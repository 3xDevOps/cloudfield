package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// A launch is admitted before anything of its workspace or of the
// account's logins is read, so a member with no added login meets the
// refusals in the order they met before logins existed: a workspace they
// cannot reach before an account not shared with them, and no read of the
// workspace's variables for a launch that is refused.
func TestALaunchIsAdmittedBeforeItReadsTheWorkspaceOrTheLogins(t *testing.T) {
	e := newAccessEnv(t, nil)
	if err := e.db.GrantWorkspace(context.Background(), e.wsA.ID, e.u.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	for _, account := range []domain.MemberID{e.a.ID, e.u.ID} {
		raw, err := json.Marshal(protocol.RunLaunchParams{
			WorkspaceID: string(e.wsA.ID), ExtraWorkspaceIDs: []string{string(e.wsB.ID)}, Task: "refused", Harness: "claude", AccountMemberID: string(account),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, perr := e.srv.dispatch(context.Background(), e.u.ID, protocol.MethodRunLaunch, raw)
		if perr == nil || perr.Code != protocol.CodeNotFound {
			t.Fatalf("U's launch on %s's account with a workspace U cannot reach: %v; want the workspace refusal first", account, perr)
		}
		e.runs.mu.Lock()
		reads := e.runs.environmentReads
		e.runs.mu.Unlock()
		if len(reads) != 0 {
			t.Fatalf("a refused launch read the variables of %v", reads)
		}
	}
}

// fakeLoginSignIns reports the sessions LoginInUse lists, fails to end
// sign-ins with endErr, records whose logins it reconciled, and records the
// logins whose volume it removed, failing with volumeErr.
type fakeLoginSignIns struct {
	LoginSignIns
	inUse      map[domain.RunID]domain.MemberID
	endErr     error
	reconciled []domain.MemberID
	volumes    []domain.LoginID
	volumeErr  error
	// reconcileErr is what ReconcileLogins fails with, when set.
	reconcileErr error
}

func (f *fakeLoginSignIns) RemoveLoginVolume(_ context.Context, _ domain.MemberID, _ string, id domain.LoginID) error {
	f.volumes = append(f.volumes, id)
	return f.volumeErr
}

func (f *fakeLoginSignIns) LoginInUse(context.Context, domain.Account) (map[domain.RunID]domain.MemberID, error) {
	return f.inUse, nil
}

func (f *fakeLoginSignIns) EndLoginSignIns(context.Context, domain.MemberID) error {
	return f.endErr
}

func (f *fakeLoginSignIns) ReconcileLogins(_ context.Context, member domain.MemberID) error {
	f.reconciled = append(f.reconciled, member)
	return f.reconcileErr
}

// A removal refused for a login in use names the owner's own sessions,
// queued subsessions included, counts the teammates', and names the
// command that moves those off it, which this server has.
func TestRemovingALoginInUseNamesTheOwnersSessionsAndCountsTheRest(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	_, bo := addMember(t, e.testEnv, "Bo", domain.RoleCollaborator, false)
	e.srv.cfg.LoginSignIns = &fakeLoginSignIns{inUse: map[domain.RunID]domain.MemberID{
		e.run.ID: e.member.ID, "run-queuedchild": e.member.ID, "run-teammate000": bo.ID,
	}}
	_, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRemove, protocol.AgentLoginParams{Agent: "claude", LoginID: "lgn-work000000"})
	own := []string{string(e.run.ID), "run-queuedchild"}
	slices.Sort(own)
	want := `login "work" is in use by your sessions ` + strings.Join(own, ", ") + ` (end them) and 1 of your teammates' sessions`
	if perr == nil || perr.Code != protocol.CodeConflict || !strings.Contains(perr.Message, want) || !strings.Contains(perr.Message, "cloudfield account revoke <member-id>") || strings.Contains(perr.Message, "use for shares") {
		t.Fatalf("removal of a login in use: %v; want %q and the revoke command", perr, want)
	}
}

// Removing a login removes the volume its sessions' mounts came from
// before its directory and row; a volume the runtime will not remove keeps
// the login, so the removal can be asked again.
func TestRemovingALoginRemovesItsVolume(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	signIns := &fakeLoginSignIns{volumeErr: errors.New("volume is in use")}
	e.srv.cfg.LoginSignIns = signIns
	params := protocol.AgentLoginParams{Agent: "claude", LoginID: "lgn-work000000"}
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRemove, params); perr == nil || !strings.Contains(perr.Message, "volume is in use") {
		t.Fatalf("removal whose volume stays: %v; want refused with the runtime's error", perr)
	}
	if _, ids := e.list(t, e.member.ID); !strings.Contains(ids, "lgn-work000000") {
		t.Fatalf("logins after the refused removal = %s; want work kept", ids)
	}
	signIns.volumeErr = nil
	if _, perr := e.call(t, e.member.ID, protocol.MethodAgentLoginRemove, params); perr != nil {
		t.Fatal(perr)
	}
	if !slices.Equal(signIns.volumes, []domain.LoginID{"lgn-work000000", "lgn-work000000"}) {
		t.Fatalf("volumes removed = %v; want work's, once per attempt", signIns.volumes)
	}
}

// Reopening a teammate's session on an added login is refused by the
// relaunch itself once its owner turned the login automatic off or off for
// shares: the session would mount it without its owner naming it. The
// refusal says which switch and what to do instead.
func TestRelaunchOnALoginTurnedAutomaticOffIsRefusedUpFront(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	_, grace := addMember(t, e.testEnv, "Grace", domain.RoleCollaborator, false)
	e.seed(t, grace.ID, "lgn-work000000", "work", "grace@work.example.test")
	if _, err := e.db.ShareAccount(ctx, grace.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, AccountMemberID: grace.ID, AccountLogin: "lgn-work000000",
		Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-login"}
	if err := e.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.UpdateRunStatus(ctx, run.ID, domain.RunMerged, "closed; retained container", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, grace.ID, "claude", "lgn-work000000", false); err != nil {
		t.Fatal(err)
	}
	_, perr := e.call(t, e.member.ID, protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: string(run.ID)})
	if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "login lgn-work000000 of this account is not available to you: its owner turned it automatic off") ||
		!strings.Contains(perr.Message, "start a new session, or ask the login's owner to turn it on again") {
		t.Fatalf("relaunch on a login turned automatic off: %v; want refused as provisioning refuses it", perr)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("scheduler calls after the refused relaunch = %v; want none", calls)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, grace.ID, "claude", "lgn-work000000", true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginShared(ctx, grace.ID, "claude", "lgn-work000000", false); err != nil {
		t.Fatal(err)
	}
	_, perr = e.call(t, e.member.ID, protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: string(run.ID)})
	if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "its owner turned it off for shares; a kept session cannot change login, so start a new session") {
		t.Fatalf("relaunch on a login turned off for shares: %v; want refused, saying so", perr)
	}
	if err := e.db.SetAgentLoginShared(ctx, grace.ID, "claude", "lgn-work000000", true); err != nil {
		t.Fatal(err)
	}
	if _, perr := e.call(t, e.member.ID, protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: string(run.ID)}); perr != nil {
		t.Fatalf("relaunch once the login is automatic again: %v", perr)
	}
}

// Removing a member removes each of their logins with its Docker volume,
// and keeps the directory of one whose volume stays, so the next start
// finds and removes both.
func TestMemberRemovalRemovesEachLoginWithItsVolume(t *testing.T) {
	for _, volumeStays := range []bool{false, true} {
		e := newLoginEnv(t)
		asSuperAdmin(t, e.testEnv)
		_, bob := addMember(t, e.testEnv, "Bob", domain.RoleCollaborator, false)
		e.seed(t, bob.ID, "lgn-work000000", "work", "bob@work.example.test")
		signIns := &fakeLoginSignIns{}
		if volumeStays {
			signIns.reconcileErr = errors.New("login lgn-work000000: volume is in use")
		}
		e.srv.cfg.LoginSignIns = signIns
		if err := controlClient(t, e.testEnv).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(bob.ID)}, nil); err != nil {
			t.Fatalf("member.remove: %v", err)
		}
		if !slices.Equal(signIns.reconciled, []domain.MemberID{bob.ID}) {
			t.Fatalf("logins reconciled for %v; want Bob's, which removes each with its volume", signIns.reconciled)
		}
		dir, _ := e.logins.Path(bob.ID, "claude", "lgn-work000000")
		if _, err := os.Lstat(dir); volumeStays != (err == nil) {
			t.Fatalf("volume stays %v: Bob's login directory: %v", volumeStays, err)
		}
	}
}

// Removing a member whose sign-in could not be ended keeps that sign-in's
// login directory for the next start, and removes every other login of the
// member now rather than keeping them all.
func TestMemberRemovalThatCannotEndASignInStillRemovesTheOtherLogins(t *testing.T) {
	e := newLoginEnv(t)
	asSuperAdmin(t, e.testEnv)
	_, bob := addMember(t, e.testEnv, "Bob", domain.RoleCollaborator, false)
	e.seed(t, bob.ID, "lgn-work000000", "work", "bob@work.example.test")
	signIns := &fakeLoginSignIns{endErr: errors.New("test: destroy unavailable")}
	e.srv.cfg.LoginSignIns = signIns
	if err := controlClient(t, e.testEnv).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(bob.ID)}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}
	if len(signIns.reconciled) != 1 || signIns.reconciled[0] != bob.ID {
		t.Fatalf("logins reconciled for %v; want Bob's, whose other logins go now", signIns.reconciled)
	}
	dir, _ := e.logins.Path(bob.ID, "claude", "lgn-work000000")
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("Bob's login directory before reconcile: %v; want it left to reconcile, not removed whole", err)
	}
}

// A launch that names no login chooses its start again under the
// authorization mutex, so a login that stopped serving while its usage was
// read is not kept.
func TestAnUnnamedStartIsChosenAgainUnderTheMutex(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	e.runs.mu.Lock()
	e.runs.startLogins = []domain.LoginID{"lgn-work000000", ""}
	e.runs.mu.Unlock()
	if _, perr := e.call(t, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude"}); perr != nil {
		t.Fatalf("run.launch: %v", perr)
	}
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	if n := len(e.runs.launchOptions); n != 1 || e.runs.launchOptions[0].AccountLogin != "" {
		t.Fatalf("launches = %+v; want one on the login chosen under the mutex, the default", e.runs.launchOptions)
	}
}

// usageByLogin reads each login's used share of its Claude five-hour
// window.
type usageByLogin map[domain.Account]float64

func (u usageByLogin) Read(ctx context.Context, member domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(member), refresh)
}

func (u usageByLogin) ReadLogin(_ context.Context, account domain.Account, _ bool) ([]quota.Provider, error) {
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{{ID: "five_hour", UsedPercent: u[account], ResetsAt: &resets}}}}, nil
}

// When the start chosen again under the mutex differs from the one whose
// usage was read, it is measured itself: a spent start then falls back to
// the next login with usage left rather than being kept unmeasured.
func TestAStartChosenAgainIsMeasuredAgain(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	e.seed(t, e.member.ID, "lgn-spare00000", "spare", "ada@spare.example.test")
	if err := e.db.SetAgentLoginAutomatic(context.Background(), e.member.ID, "claude", "lgn-work000000", false); err != nil {
		t.Fatal(err)
	}
	installUsageStub(e.testEnv, usageByLogin{
		{Member: e.member.ID, Login: "lgn-work000000"}: 10, domain.AccountOf(e.member.ID): 100, {Member: e.member.ID, Login: "lgn-spare00000"}: 20,
	})
	e.runs.mu.Lock()
	e.runs.startLogins = []domain.LoginID{"lgn-work000000", ""}
	e.runs.mu.Unlock()
	if _, perr := e.call(t, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude"}); perr != nil {
		t.Fatalf("run.launch: %v", perr)
	}
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	if n := len(e.runs.launchOptions); n != 1 || e.runs.launchOptions[0].AccountLogin != "lgn-spare00000" {
		t.Fatalf("launches = %+v; want one on spare, past the spent default chosen under the mutex", e.runs.launchOptions)
	}
}

// A launch that names no login and starts on one billed per use takes a
// login with readable usage left first; one that names the per-use login
// keeps it.
func TestAnUnnamedStartBilledPerUseYieldsToReadableUsage(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-console000", "console", "ada@console.example.test")
	installUsageStub(e.testEnv, usageByLogin{domain.AccountOf(e.member.ID): 10})
	e.runs.mu.Lock()
	e.runs.startLogins = []domain.LoginID{"lgn-console000", "lgn-console000"}
	e.runs.perUse = map[domain.LoginID]bool{"lgn-console000": true}
	e.runs.mu.Unlock()
	for _, named := range []string{"", "lgn-console000"} {
		if _, perr := e.call(t, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude", Login: named}); perr != nil {
			t.Fatalf("run.launch naming %q: %v", named, perr)
		}
	}
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	if n := len(e.runs.launchOptions); n != 2 || e.runs.launchOptions[0].AccountLogin != "" || e.runs.launchOptions[1].AccountLogin != "lgn-console000" {
		t.Fatalf("launches = %+v; want the unnamed one on the default login and the named one on console", e.runs.launchOptions)
	}
}

// A launch that names an added login where a workspace variable signs the
// agent in is refused with the reason, never moved to another login.
func TestANamedLoginAVariableWouldPayForIsRefused(t *testing.T) {
	e := newLoginEnv(t)
	e.seed(t, e.member.ID, "lgn-work000000", "work", "ada@work.example.test")
	e.runs.mu.Lock()
	e.runs.environmentPays = true
	e.runs.mu.Unlock()
	_, perr := e.call(t, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude", Login: "lgn-work000000"})
	if perr == nil || perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "login lgn-work000000 would not pay") {
		t.Fatalf("run.launch naming work where a variable pays: %v; want refused with the reason", perr)
	}
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	if len(e.runs.launchOptions) != 0 {
		t.Fatalf("launches = %+v; want none", e.runs.launchOptions)
	}
}
