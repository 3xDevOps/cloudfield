//go:build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// stubLoginUsage reports each login's used share of its Claude five-hour
// window; a login it has no figure for reads 0%.
type stubLoginUsage struct {
	mu   sync.Mutex
	used map[domain.Account]float64
}

func (u *stubLoginUsage) set(account domain.Account, used float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used[account] = used
}

func (u *stubLoginUsage) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(account), refresh)
}

func (u *stubLoginUsage) ReadLogin(_ context.Context, account domain.Account, _ bool) ([]quota.Provider, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u.used[account], ResetsAt: &resets},
	}}}, nil
}

// addLogin settles an added login for member as a finished sign-in would,
// with made-up values.
func addLogin(ctx context.Context, t *testing.T, srv *coordServer, dataDir string, member domain.MemberID, id domain.LoginID, name string) {
	t.Helper()
	logins, err := memberhome.NewLogins(filepath.Join(dataDir, "logins"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := logins.Add(member, "claude", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, memberhome.LoginCredentials), []byte(`{"claudeAiOauth":{"accessToken":"test-access-`+name+`","refreshToken":"test-refresh-`+name+`","expiresAt":4102444800000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := logins.Settle(member, "claude", id, memberhome.LoginIdentity{Kind: memberhome.LoginSubscription, Plan: "max"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.srv.Store().AddAgentLogin(ctx, domain.AgentLogin{MemberID: member, Agent: "claude", ID: id, Name: name, CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationSpentFirstLoginLaunchesOnTheSecond: Ada keeps a second
// login. Her default login is spent, so a launch that names no login starts
// on the second one, whose files the container mounts, and the session
// says so.
func TestIntegrationSpentFirstLoginLaunchesOnTheSecond(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	second := domain.LoginID("lgn-second0000")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, second, "second")
	usage.set(domain.AccountOf(e.ada.id), 100)
	usage.set(domain.Account{Member: e.ada.id, Login: second}, 10)

	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "second login launch", Harness: "claude", Mode: string(domain.LaunchHeadless),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if launched.Run.AccountMemberID != string(e.ada.id) || launched.Run.AccountLogin != string(second) || launched.Run.AccountLoginName != "second" || launched.Run.AccountLoginPerUse {
		t.Fatalf("run account %s login %q (%q); want Ada's login second", launched.Run.AccountMemberID, launched.Run.AccountLogin, launched.Run.AccountLoginName)
	}
	var c *e2eContainer
	deadline := time.Now().Add(time.Minute)
	for c == nil && time.Now().Before(deadline) {
		c = rt.container(launched.Run.ID)
		time.Sleep(50 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("no container for the run")
	}
	dir := filepath.Join(e.dataDir, "logins", string(e.ada.id), "claude", string(second))
	found := false
	for _, m := range c.spec.Mounts {
		if m.HostPath == dir && m.Subpath == memberhome.LoginCredentials {
			found = true
		}
	}
	if !found {
		t.Fatalf("the container does not mount login second's credentials as a subpath; mounts %+v", c.spec.Mounts)
	}

	// Only a login's owner names it: Bo, who holds a share, cannot.
	if _, err := srv.srv.Store().ShareAccount(ctx, e.ada.id, e.bo.id); err != nil {
		t.Fatal(err)
	}
	boCtrl, boClient := srv.control(t, e.bo.key)
	defer boClient.Close()
	err := boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "named by a teammate", Harness: "claude", Mode: string(domain.LaunchHeadless),
		AccountMemberID: string(e.ada.id), Login: string(second),
	}, &launched)
	if err == nil || !strings.Contains(err.Error(), "only a login's owner names it") {
		t.Fatalf("Bo naming Ada's login: %v; want the refusal", err)
	}
}

// headlessLimit runs a headless session that reports text as its usage
// limit and exits, then resumes with the continuation continued.
func headlessLimit(t *testing.T, rt *e2eRuntime, task, text, continued string) {
	t.Helper()
	limit, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", "claude_usage_limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := string(limit)
	if text != "" {
		fixture = strings.ReplaceAll(fixture, "You've hit your", text)
	}
	rt.script(task, func(c *e2eContainer) {
		c.output(strings.ReplaceAll(fixture, "\n", "\r\n"))
		time.Sleep(500 * time.Millisecond)
		c.exitNow(1)
	})
	if continued != "" {
		rt.script(continued, func(c *e2eContainer) {
			c.output("resumed\r\n")
			time.Sleep(time.Minute)
		})
	}
}

func waitLoginRun(t *testing.T, ctrl interface {
	Call(string, any, any) error
}, id string, done func(protocol.Run) bool) protocol.Run {
	t.Helper()
	var run protocol.RunResult
	deadline := time.Now().Add(time.Minute)
	for {
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: id}, &run); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		if done(run.Run) {
			return run.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s on %s login %q (%s)", run.Run.Status, run.Run.AccountMemberID, run.Run.AccountLogin, run.Run.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestIntegrationHeadlessMovesToTheSecondLogin: Ada's headless session on
// her default login hits its usage limit; it continues on her second login
// in a new container that resumes the agent's conversation, the account
// segments record both logins, and the room note names them.
func TestIntegrationHeadlessMovesToTheSecondLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	second := domain.LoginID("lgn-second0000")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, second, "second")
	usage.set(domain.AccountOf(e.ada.id), 50)
	usage.set(domain.Account{Member: e.ada.id, Login: second}, 10)
	const task = "run until the first login runs out"
	headlessLimit(t, rt, task, "", "Your previous turn stopped because Ada's usage ran out. The session continues on Ada (second)'s account. Continue the task where you stopped.")

	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", Mode: string(domain.LaunchHeadless), Login: "default",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	moved := waitLoginRun(t, ctrl, launched.Run.ID, func(r protocol.Run) bool { return r.AccountLogin == string(second) })
	if moved.Status != string(domain.RunRunning) || moved.AccountLoginName != "second" {
		t.Fatalf("moved run %s login name %q; want running on second", moved.Status, moved.AccountLoginName)
	}
	segments, err := srv.srv.Store().ListRunAccountSegments(ctx, domain.RunID(launched.Run.ID))
	if err != nil || len(segments) != 2 || segments[0].Account() != domain.AccountOf(e.ada.id) || segments[1].Account() != (domain.Account{Member: e.ada.id, Login: second}) {
		t.Fatalf("segments = %+v, %v; want the default login then second", segments, err)
	}
	deadline := time.Now().Add(time.Minute)
	for noted := false; !noted; {
		page, err := srv.srv.Store().ListRoomMessages(ctx, e.ws.ID, domain.RunID(launched.Run.ID), "", 50)
		if err != nil {
			t.Fatal(err)
		}
		noted = slices.ContainsFunc(page.Items, func(m *store.RoomMessage) bool {
			return m.Kind == store.RoomMessageSystem && m.Body == "Continued on Ada (second)'s account because Ada's usage ran out."
		})
		if !noted && time.Now().After(deadline) {
			t.Fatal("the room has no note of the move")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestIntegrationSeatLimitStays: a seat message is a
// restriction on the account, not a spent balance, so Ada's session does not
// move to her second login; with no other account it stops with the reason.
func TestIntegrationSeatLimitStays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	second := domain.LoginID("lgn-second0000")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, second, "second")
	const task = "run until a seat restriction"
	headlessLimit(t, rt, task, "Your seat type doesn't include usage", "")
	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", Mode: string(domain.LaunchHeadless), Login: "default",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	ended := waitLoginRun(t, ctrl, launched.Run.ID, func(r protocol.Run) bool { return r.Status == string(domain.RunFailed) })
	if ended.AccountLogin != "" || !strings.HasPrefix(ended.Reason, "Usage limit reached on Ada's account") {
		t.Fatalf("run failed on login %q with %q; want it stopped on the default login", ended.AccountLogin, ended.Reason)
	}
}

// TestIntegrationATeammatesSignalIsChecked: Bo's session on Ada's shared
// added login reports a usage limit that a fresh read of the login does not
// show, so it does not move and stops with that reason.
func TestIntegrationATeammatesSignalIsChecked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	work := domain.LoginID("lgn-work000000")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, work, "work")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, "lgn-spare00000", "spare")
	if _, err := srv.srv.Store().ShareAccount(ctx, e.ada.id, e.bo.id); err != nil {
		t.Fatal(err)
	}
	if err := srv.srv.Store().SetAgentLoginShared(ctx, e.ada.id, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	usage.set(domain.Account{Member: e.ada.id, Login: work}, 40)
	const task = "report a limit that is not there"
	headlessLimit(t, rt, task, "", "")
	ctrl, client := srv.control(t, e.bo.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", Mode: string(domain.LaunchHeadless), AccountMemberID: string(e.ada.id),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if launched.Run.AccountLogin != string(work) {
		t.Fatalf("Bo's launch on Ada's account started on %q; want work, her first shared login", launched.Run.AccountLogin)
	}
	ended := waitLoginRun(t, ctrl, launched.Run.ID, func(r protocol.Run) bool { return r.Status == string(domain.RunFailed) })
	if ended.AccountLogin != string(work) || !strings.Contains(ended.Reason, "the agent reported a usage limit that Ada (work)'s usage does not show") {
		t.Fatalf("run failed on %q with %q; want it kept on work with the checked reason", ended.AccountLogin, ended.Reason)
	}
}
