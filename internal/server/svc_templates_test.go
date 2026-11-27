package server

import (
	"context"
	"sync"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/sshd"
)

// lockedRuns records whether the launch mutex was held when a template's
// login was chosen, when its launch began, and once its run was admitted.
type lockedRuns struct {
	sshd.RunController
	mu                               *sync.Mutex
	atStart, atLaunch, afterAdmitted bool
}

func held(mu *sync.Mutex) bool {
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
}

func (r *lockedRuns) StartLogin(context.Context, domain.WorkspaceID, domain.MemberID, domain.MemberID, string, domain.LaunchMode, domain.Permission) (domain.LoginID, error) {
	r.atStart = held(r.mu)
	return "lgn-work000000", nil
}

func (r *lockedRuns) LaunchWithOptions(_ context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	r.atLaunch = held(r.mu)
	opts.Admitted()
	r.afterAdmitted = held(r.mu)
	return &domain.Run{ID: "run-template", WorkspaceID: ws, MemberID: member, AccountMemberID: account, AccountLogin: opts.AccountLogin, Task: task, Harness: harness, Mode: mode}, nil
}

// A template launch holds the launch mutex from choosing its login until
// its run is stored, as run.launch does, so the login cannot be removed in
// between.
func TestATemplateLaunchHoldsTheLaunchMutexUntilItsRunIsStored(t *testing.T) {
	mu := &sync.Mutex{}
	runs := &lockedRuns{mu: mu}
	run, err := guardedRuns{ssh: &sshd.Config{Runs: runs, AuthorizationMu: mu}}.LaunchWithOptions(t.Context(), "ws-1", "mem-1", "mem-1", "t", "claude", domain.LaunchTUI, domain.LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.AccountLogin != "lgn-work000000" || !runs.atStart || !runs.atLaunch || runs.afterAdmitted {
		t.Fatalf("login %q; mutex held at the choice %v, at the launch %v, once admitted %v; want work, held, held, released", run.AccountLogin, runs.atStart, runs.atLaunch, runs.afterAdmitted)
	}
	if held(mu) {
		t.Fatal("the mutex is still held after the launch")
	}
}
