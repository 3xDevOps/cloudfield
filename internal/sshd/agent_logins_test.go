package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"golang.org/x/crypto/ssh"
)

type loginEnv struct {
	*testEnv
	logins *memberhome.Logins
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Logins, c.Homes = logins, homes })
	return &loginEnv{testEnv: e, logins: logins}
}

// seed adds a settled login as a finished sign-in would.
func (e *loginEnv) seed(t *testing.T, member domain.MemberID, id domain.LoginID, name, email string) {
	t.Helper()
	dir, err := e.logins.Add(member, "claude", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, memberhome.LoginCredentials), []byte(`{"claudeAiOauth":{"accessToken":"test-access-`+name+`","refreshToken":"test-refresh-`+name+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.logins.Settle(member, "claude", id, memberhome.LoginIdentity{Kind: memberhome.LoginSubscription, Plan: "max", Email: email, OrganizationID: "org-" + name}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.AddAgentLogin(context.Background(), domain.AgentLogin{MemberID: member, Agent: "claude", ID: id, Name: name, CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
}

func (e *loginEnv) call(t *testing.T, member domain.MemberID, method string, params any) (json.RawMessage, *protocol.Error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return e.srv.dispatch(context.Background(), member, method, raw)
}

func (e *loginEnv) list(t *testing.T, member domain.MemberID) (protocol.AgentLoginListResult, string) {
	t.Helper()
	raw, perr := e.call(t, member, protocol.MethodAgentLoginList, protocol.AgentLoginParams{Agent: "claude"})
	if perr != nil {
		t.Fatalf("agent.login.list: %v", perr)
	}
	var out protocol.AgentLoginListResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

func ids(logins []protocol.AgentLogin) string {
	var out []string
	for _, l := range logins {
		out = append(out, l.Name)
	}
	return strings.Join(out, ",")
}

// A member who added no login lists the default one, and the listing writes
// no row and no file.
func TestAgentLoginListWithoutAddedLoginsWritesNothing(t *testing.T) {
	e := newLoginEnv(t)
	out, _ := e.list(t, e.member.ID)
	if len(out.Logins) != 1 || out.Logins[0].ID != "default" || !out.Logins[0].Automatic || !out.Logins[0].Shared {
		t.Fatalf("logins = %+v; want the default login alone", out.Logins)
	}
	if rows, err := e.db.ListAgentLogins(context.Background(), e.member.ID, "claude"); err != nil || len(rows) != 0 {
		t.Fatalf("rows after listing = %v, %v; want none", rows, err)
	}
	if _, err := os.Lstat(e.logins.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("logins root after listing: %v; want none", err)
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginOrder, protocol.AgentLoginOrderParams{Agent: "claude", LoginIDs: []string{"default"}}); perr == nil {
		t.Fatal("ordering with no added login succeeded")
	}
	if rows, _ := e.db.ListAgentLogins(context.Background(), e.member.ID, "claude"); len(rows) != 0 {
		t.Fatalf("rows after a refused order = %v; want none", rows)
	}
}

// The owner manages their logins; nothing returned carries a credential,
// and another member's login id is not found.
func TestAgentLoginMethodsActOnTheCallersOwnLogins(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	bo := &domain.Member{DisplayName: "Bo", PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(ctx, bo); err != nil {
		t.Fatal(err)
	}
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	e.seed(t, e.member.ID, "lgn-spare00000", "spare", "ada@spare.example.test")

	out, raw := e.list(t, e.member.ID)
	if ids(out.Logins) != "default,work,spare" || out.Logins[1].Email != "ada@work.example.test" || out.Logins[1].Kind != memberhome.LoginSubscription {
		t.Fatalf("logins = %+v", out.Logins)
	}
	if strings.Contains(raw, "test-access") || strings.Contains(raw, "test-refresh") {
		t.Fatalf("agent.login.list carries a credential: %s", raw)
	}
	if other, _ := e.list(t, bo.ID); ids(other.Logins) != "default" {
		t.Fatalf("Bo sees %q; want only his default login", ids(other.Logins))
	}

	for _, tc := range []struct {
		method string
		params any
		code   int
	}{
		{protocol.MethodAgentLoginRename, protocol.AgentLoginRenameParams{Agent: "claude", LoginID: "lgn-work000000", Name: "mine"}, protocol.CodeNotFound},
		{protocol.MethodAgentLoginRemove, protocol.AgentLoginParams{Agent: "claude", LoginID: "lgn-work000000"}, protocol.CodeNotFound},
		{protocol.MethodAccountUsage, protocol.AccountUsageParams{AccountMemberID: string(e.member.ID), Login: "lgn-work000000"}, protocol.CodeDenied},
	} {
		if tc.method == protocol.MethodAccountUsage {
			installUsageStub(e.testEnv, &loginUsageStub{})
			if _, err := e.db.ShareAccount(ctx, e.member.ID, bo.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, perr := e.call(t, bo.ID, tc.method, tc.params); perr == nil || perr.Code != tc.code {
			t.Errorf("Bo calling %s on Ada's login: %v; want code %d", tc.method, perr, tc.code)
		}
	}

	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRename, protocol.AgentLoginRenameParams{Agent: "claude", LoginID: "lgn-spare00000", Name: "work"}); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("rename onto a taken name: %v; want a conflict", perr)
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRename, protocol.AgentLoginRenameParams{Agent: "claude", LoginID: "default", Name: "home"}); perr == nil {
		t.Fatal("renaming the default login succeeded")
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginOrder, protocol.AgentLoginOrderParams{Agent: "claude", LoginIDs: []string{"lgn-spare00000", "default", "lgn-work000000"}}); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginAutomatic, protocol.AgentLoginAutomaticParams{Agent: "claude", LoginID: "lgn-work000000"}); perr != nil {
		t.Fatal(perr)
	}
	if out, _ := e.list(t, e.member.ID); ids(out.Logins) != "spare,default,work" || out.Logins[2].Automatic {
		t.Fatalf("after order and automatic off: %+v", out.Logins)
	}
	for _, id := range []string{"lgn-work000000", "lgn-spare00000"} {
		if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRemove, protocol.AgentLoginParams{Agent: "claude", LoginID: id}); perr != nil {
			t.Fatal(perr)
		}
		dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(id))
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("login %s's directory after removal: %v; want it gone", id, err)
		}
	}
	if rows, err := e.db.ListAgentLogins(ctx, e.member.ID, "claude"); err != nil || len(rows) != 0 {
		t.Fatalf("rows after removing every added login = %v, %v; want none", rows, err)
	}
}

type loginUsageStub struct {
	usageReaderStub
	read []domain.Account
}

func (r *loginUsageStub) ReadLogin(_ context.Context, account domain.Account, _ bool) ([]quota.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read = append(r.read, account)
	return usageProviderFixture(), nil
}

// account.usage reads the owner's own added login by id, and a login the
// owner does not have is not found.
func TestAccountUsageReadsTheOwnersAddedLogin(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	stub := &loginUsageStub{}
	installUsageStub(e.testEnv, stub)
	result, perr := dispatchUsage(t, e.testEnv, e.member.ID, usageParams(t, protocol.AccountUsageParams{Login: "lgn-work000000"}))
	if perr != nil || result.Login != "lgn-work000000" || len(stub.read) != 1 || stub.read[0] != (domain.Account{Member: e.member.ID, Login: "lgn-work000000"}) {
		t.Fatalf("usage = %+v, %v, read %v; want the added login read", result, perr, stub.read)
	}
	if _, perr := dispatchUsage(t, e.testEnv, e.member.ID, usageParams(t, protocol.AccountUsageParams{Login: "lgn-gone000000"})); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("usage of a login Ada does not have: %v; want not found", perr)
	}
}

// Without the sign-in seam the server says it keeps no added logins.
func TestAgentLoginAddNeedsTheSignInSeam(t *testing.T) {
	e := newLoginEnv(t)
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginAdd, protocol.AgentLoginAddParams{Agent: "claude", Name: "work"}); perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("agent.login.add without the seam: %v; want unavailable", perr)
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginAdd, protocol.AgentLoginAddParams{Agent: "claude", Name: "Default"}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("agent.login.add with a bad name: %v; want invalid params", perr)
	}
}

// Only Claude Code keeps added logins: a method naming another agent is
// refused rather than answered with a made-up default login.
func TestAgentLoginMethodsNameClaudeOnly(t *testing.T) {
	e := newLoginEnv(t)
	for _, agent := range []string{"", "codex", "Claude"} {
		if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginList, protocol.AgentLoginParams{Agent: agent}); perr == nil || perr.Code != protocol.CodeInvalidParams {
			t.Errorf("agent.login.list for %q: %v; want invalid params", agent, perr)
		}
	}
}
