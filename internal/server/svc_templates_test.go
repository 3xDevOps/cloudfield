package server

import (
	"context"
	"github.com/3xDevOps/cloudfield/internal/ctxlock"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/sshd"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

// lockedRuns records whether the launch mutex was held each time a
// template's login was chosen, when its launch began, and once its run was
// admitted.
type lockedRuns struct {
	sshd.RunController
	mu                      *ctxlock.Mutex
	starts                  []bool
	atLaunch, afterAdmitted bool
}

func held(mu *ctxlock.Mutex) bool {
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
}

func (r *lockedRuns) StartLogin(context.Context, domain.WorkspaceID, domain.MemberID, domain.MemberID, string, domain.LaunchMode, domain.Permission) (domain.LoginID, error) {
	r.starts = append(r.starts, held(r.mu))
	return "lgn-work000000", nil
}

func (r *lockedRuns) LoginUsageUnknown(context.Context, domain.MemberID, domain.Account, string) (bool, error) {
	return false, nil
}

func (r *lockedRuns) LaunchWithOptions(_ context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	r.atLaunch = held(r.mu)
	if opts.Admitted != nil {
		opts.Admitted()
	}
	r.afterAdmitted = held(r.mu)
	return &domain.Run{ID: "run-template", WorkspaceID: ws, MemberID: member, AccountMemberID: account, AccountLogin: opts.AccountLogin, Task: task, Harness: harness, Mode: mode}, nil
}

// A template launch by a member who keeps logins chooses its login before
// the launch mutex, for the usage read, and again under it, which it holds
// until the run is stored, as run.launch does, so the login cannot be
// removed in between. A member who keeps none launches as before logins
// existed, without the mutex.
func TestATemplateLaunchHoldsTheLaunchMutexUntilItsRunIsStored(t *testing.T) {
	db, err := storetest.Open(filepath.Join(t.TempDir(), "templates.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ada := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := db.CreateMember(t.Context(), ada); err != nil {
		t.Fatal(err)
	}
	mu := &ctxlock.Mutex{}
	runs := &lockedRuns{mu: mu}
	launch := func() *domain.Run {
		t.Helper()
		run, err := guardedRuns{ssh: &sshd.Config{Runs: runs, Store: db, AuthorizationMu: mu}}.LaunchWithOptions(t.Context(), "ws-1", ada.ID, ada.ID, "t", "claude", domain.LaunchTUI, domain.LaunchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if held(mu) {
			t.Fatal("the mutex is still held after the launch")
		}
		return run
	}
	if run := launch(); run.AccountLogin != "" || len(runs.starts) != 0 || runs.atLaunch {
		t.Fatalf("login %q, choices %v, mutex held at the launch %v; want the default login, no choice, no mutex", run.AccountLogin, runs.starts, runs.atLaunch)
	}
	if _, err := db.AddAgentLogin(t.Context(), domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-work000000", Name: "work", CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
	run := launch()
	if run.AccountLogin != "lgn-work000000" || !slices.Equal(runs.starts, []bool{false, true}) || !runs.atLaunch || runs.afterAdmitted {
		t.Fatalf("login %q; mutex held at the choices %v, at the launch %v, once admitted %v; want work, chosen outside then under it, held, released", run.AccountLogin, runs.starts, runs.atLaunch, runs.afterAdmitted)
	}
}
