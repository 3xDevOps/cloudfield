//go:build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
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

	// Before a share, Bo learns nothing about Ada's logins: the launch is
	// refused for the share alone.
	boCtrl0, boClient0 := srv.control(t, e.bo.key)
	defer boClient0.Close()
	if err := boCtrl0.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "no share", Harness: "claude", Mode: string(domain.LaunchHeadless), AccountMemberID: string(e.ada.id),
	}, &launched); err == nil || !strings.Contains(err.Error(), "has not shared their account with you") {
		t.Fatalf("Bo's launch without a share: %v; want the share refusal", err)
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

// TestIntegrationRunningSessionsStayOnTheirLogin: until moves between an
// account's logins check what such a move needs, a running session that
// runs out on Ada's default login does not move to her second login.
func TestIntegrationRunningSessionsStayOnTheirLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, "lgn-second0000", "second")
	limit, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", "claude_usage_limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	const task = "run out on the default login"
	rt.script(task, func(c *e2eContainer) {
		c.output(strings.ReplaceAll(string(limit), "\n", "\r\n"))
		time.Sleep(500 * time.Millisecond)
		c.exitNow(1)
	})
	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", Mode: string(domain.LaunchHeadless), Login: "default",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	var run protocol.RunResult
	deadline := time.Now().Add(time.Minute)
	for run.Run.Status != string(domain.RunFailed) {
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: launched.Run.ID}, &run); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s on login %q (%s); want it failed on the default login", run.Run.Status, run.Run.AccountLogin, run.Run.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Run.AccountLogin != "" || !strings.HasPrefix(run.Run.Reason, "Usage limit reached on Ada's account") {
		t.Fatalf("run failed on login %q with %q; want the default login and the usage-limit reason", run.Run.AccountLogin, run.Run.Reason)
	}
}
