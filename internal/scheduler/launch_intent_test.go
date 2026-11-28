package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func (e *testEnv) waitingIntent(t *testing.T) *domain.LaunchIntent {
	t.Helper()
	in, _, err := e.db.CreateLaunchIntent(t.Context(), &domain.LaunchIntent{
		MemberID: e.member.ID, WorkspaceID: e.ws.ID, Method: "run.launch", Params: []byte(`{}`),
		Base:    domain.LaunchBase{Commit: testBaseCommit, Branch: "kept", Source: "origin", CheckedAt: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)},
		Refusal: "no room",
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// A waiting launch starts as the session whose ID it reserved, from the
// base it kept, and only while an attempt holds it.
func TestAWaitingLaunchStartsAsItsReservedSessionFromItsKeptBase(t *testing.T) {
	e := newTestEnv(t, nil)
	in := e.waitingIntent(t)
	start := func() (*domain.Run, error) {
		return e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "waited", "fake", domain.LaunchTUI,
			domain.LaunchOptions{ReservedRunID: in.ID, Intent: true, Base: &in.Base, Retry: true})
	}
	if _, err := start(); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a start no attempt claimed = %v, want it refused", err)
	}
	if _, err := e.db.GetRun(t.Context(), in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("its session = %v, want none", err)
	}
	if err := e.db.ClaimLaunchIntent(t.Context(), in.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := start()
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != in.ID || run.BaseCommit != in.Base.Commit || run.BaseBranch != "kept" || run.BaseSource != "origin" || !run.BaseCheckedAt.Equal(in.Base.CheckedAt) {
		t.Fatalf("session = %+v, want the reserved ID and the kept base", run)
	}
	if got, err := e.db.GetLaunchIntent(t.Context(), in.ID); err != nil || got.State != domain.IntentStarted {
		t.Fatalf("intent after the start = %+v, %v", got, err)
	}
	// A second start of it is the same session, not another.
	again, err := start()
	if err != nil || again.ID != in.ID {
		t.Fatalf("a second start = %+v, %v; want the same session", again, err)
	}
	if c := e.rt.byName(string(in.ID)); c == nil {
		t.Fatal("no container for the started session")
	}
}

// A launch that asked to wait goes behind the launches already waiting
// without an admission of its own; one that did not is admitted, and is
// counted against those it passed.
func TestALaunchQueuesBehindOrPassesTheWaitingOnes(t *testing.T) {
	e := newTestEnv(t, nil)
	in := e.waitingIntent(t)
	_, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "asks to wait", "fake", domain.LaunchTUI, domain.LaunchOptions{Queue: true})
	if !errors.Is(err, ErrLaunchesWaiting) || !IsCapacityRefusal(err) {
		t.Fatalf("a launch that asks to wait while one waits = %v, want it sent behind", err)
	}
	if got, _ := e.db.GetLaunchIntent(t.Context(), in.ID); got.Passed != 0 {
		t.Fatalf("passed = %d, want 0: nothing was admitted", got.Passed)
	}
	e.launchFake(t, "does not wait")
	if got, _ := e.db.GetLaunchIntent(t.Context(), in.ID); got.Passed != 1 {
		t.Fatalf("passed = %d, want 1: a launch was admitted ahead of it", got.Passed)
	}
	e.counted(t, domain.CapacityPass, opLaunch, "")
	if err := e.db.EndLaunchIntent(t.Context(), in.ID, domain.IntentCancelled, "done", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "nothing waits", "fake", domain.LaunchTUI, domain.LaunchOptions{Queue: true}); err != nil {
		t.Fatalf("a launch that asks to wait with nothing waiting = %v, want it admitted", err)
	}
}
