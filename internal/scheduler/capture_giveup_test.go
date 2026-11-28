package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// stoppedVM closes a saved session VM that can neither wake nor boot, so its
// uncommitted work stays in the stopped VM and the close fails with the
// boot's error.
func stoppedVM(t *testing.T, mutate func(*Config)) (*testEnv, *checkoutVMs, *domain.Run, func() time.Time, func(time.Duration)) {
	t.Helper()
	clock, advance := movableClock()
	e, vr, _, _ := newVMEnv(t, func(cfg *Config) {
		cfg.Now = clock
		if mutate != nil {
			mutate(cfg)
		}
	})
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.saveRun(t, run.ID)
	vr.saveMu.Lock()
	vr.restoreErr = errors.New("restore: monitor exited")
	vr.saveMu.Unlock()
	vr.mu.Lock()
	vr.bootErr = errors.New("boot: no space left on device")
	vr.mu.Unlock()
	if err := e.end(t, run.ID, "close"); err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("close of a VM that cannot boot = %v, want the boot's error", err)
	}
	e.waitTerminal(t, run.ID)
	return e, vr, run, clock, advance
}

// givenUpStoppedVM is stoppedVM polled until its capture is given up, an
// hour into the backoff of the last boot.
func givenUpStoppedVM(t *testing.T, mutate func(*Config)) (*testEnv, *checkoutVMs, *domain.Run) {
	t.Helper()
	e, vr, run, _, advance := stoppedVM(t, mutate)
	for range 5 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	advance(time.Hour)
	e.sched.sweepRetained(t.Context())
	if got := vr.boots.Load(); got != 6 {
		t.Fatalf("%d capture boots, want 6 and then no more", got)
	}
	return e, vr, run
}

// A cause that lasts hours, such as a full data disk, uses up the six tries.
// Deleting the session afterwards threw the VM's work away without one more
// try, although the capture would have worked by then.
func TestDeletingAGivenUpVMTriesItsCaptureOnceMore(t *testing.T) {
	t.Parallel()
	for _, recovered := range []bool{true, false} {
		t.Run(fmt.Sprintf("capture works now %t", recovered), func(t *testing.T) {
			t.Parallel()
			e, vr, run := givenUpStoppedVM(t, nil)
			commits := 0
			if recovered {
				vr.mu.Lock()
				vr.bootErr = nil
				vr.mu.Unlock()
				commits = 1
			}
			published := e.git.publishedCount(run.ID)
			if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
				t.Fatalf("delete of the session given up on: %v", err)
			}
			if got := e.git.commitsFor(run.ID); len(got) != commits || vr.boots.Load() != 7 || e.git.publishedCount(run.ID) != published+commits {
				t.Fatalf("commits %q after %d capture boots, %d publishes more; want one more boot and %d commit published",
					got, vr.boots.Load(), e.git.publishedCount(run.ID)-published, commits)
			}
			if e.rt.byName(string(run.ID)) != nil {
				t.Fatal("delete kept the VM of the session given up on")
			}
		})
	}
}

// A host too short of memory to boot the VM has not tried the capture, so a
// delete does not go ahead without it, and says when it will.
func TestDeletingAGivenUpVMWaitsForTheMemoryToBootIt(t *testing.T) {
	t.Parallel()
	var short atomic.Bool
	e, vr, run := givenUpStoppedVM(t, func(cfg *Config) {
		cfg.hostMemory = func() (uint64, uint64, error) {
			if short.Load() {
				return 32 << 30, 1 << 20, nil
			}
			return 32 << 30, 24 << 30, nil
		}
	})
	vr.mu.Lock()
	vr.bootErr = nil
	vr.mu.Unlock()
	short.Store(true)
	err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID)
	if !errors.Is(err, ErrMemoryPressure) || !strings.Contains(err.Error(), "delete the session again once the host has the memory") {
		t.Fatalf("delete while the host cannot boot the VM = %v, want the memory refusal and when a delete works", err)
	}
	if e.rt.byName(string(run.ID)) == nil || vr.boots.Load() != 6 {
		t.Fatalf("VM kept %t after %d boots; want it kept with no boot", e.rt.byName(string(run.ID)) != nil, vr.boots.Load())
	}
	short.Store(false)
	if err = e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("delete with memory back: %v", err)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || e.rt.byName(string(run.ID)) != nil {
		t.Fatalf("commits %q, VM kept %t; want the work committed and the VM removed", got, e.rt.byName(string(run.ID)) != nil)
	}
}

// Before the capture is given up a delete fails with the capture's error,
// which said nothing of the tries left or of what a delete does after them.
func TestADeleteRefusedForItsCaptureSaysWhenItWillWork(t *testing.T) {
	t.Parallel()
	e, vr, run, clock, _ := stoppedVM(t, nil)
	err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID)
	if err == nil {
		t.Fatal("delete of a closed VM whose work was never committed went ahead")
	}
	for _, want := range []string{
		"the VM is not running",
		"1 of 6 scheduled tries",
		"after all 6 have failed, a delete goes ahead once it has made one more try",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("delete refused with %q, want it to contain %q", err, want)
		}
	}
	if next := clock().Add(cleanupRetryBase).Format(time.RFC3339); strings.Count(err.Error(), next) != 1 {
		t.Errorf("delete refused with %q, want the boot's next try at %s named once", err, next)
	}
	if e.rt.byName(string(run.ID)) == nil || vr.boots.Load() != 1 {
		t.Fatalf("VM kept %t after %d boots; want it kept and no boot inside the backoff", e.rt.byName(string(run.ID)) != nil, vr.boots.Load())
	}
}

// Before the capture is given up, memory alone does not let a delete go
// ahead: the capture must work, or its scheduled tries all fail. A delete
// refused for memory said to delete again once the host had it, and that
// delete was refused too.
func TestADeleteShortOfMemoryBeforeTheGiveUpNamesTheTriesLeft(t *testing.T) {
	t.Parallel()
	var short atomic.Bool
	e, vr, run, clock, advance := stoppedVM(t, func(cfg *Config) {
		cfg.hostMemory = func() (uint64, uint64, error) {
			if short.Load() {
				return 32 << 30, 1 << 20, nil
			}
			return 32 << 30, 24 << 30, nil
		}
	})
	advance(cleanupRetryBase)
	tail := "1 of 6 scheduled tries at this capture have failed, the next no sooner than " + clock().Add(cleanupRetryBase).Format(time.RFC3339)
	short.Store(true)
	err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID)
	if !errors.Is(err, ErrMemoryPressure) || !strings.Contains(err.Error(), tail) || strings.Contains(err.Error(), "delete the session again") {
		t.Fatalf("delete while the host cannot boot the VM = %v, want the memory refusal with %q and no promise that memory alone deletes it", err, tail)
	}
	short.Store(false)
	err = e.sched.DeleteRun(t.Context(), run.ID, e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "no space left on device") || !strings.Contains(err.Error(), tail) {
		t.Fatalf("delete with memory back = %v, want its boot's error with %q: its own try is not a scheduled one", err, tail)
	}
	if e.rt.byName(string(run.ID)) == nil || vr.boots.Load() != 2 {
		t.Fatalf("VM kept %t after %d boots; want it kept after the delete's one boot", e.rt.byName(string(run.ID)) != nil, vr.boots.Load())
	}
}

// A member's Free sandbox between two periodic tries boots the VM and moves
// the boot's own backoff past theirs. The next periodic try then only found
// that backoff waiting, and counted it as a failed try.
func TestACaptureWaitingForItsBootIsNotAFailedTry(t *testing.T) {
	t.Parallel()
	var short atomic.Bool
	e, vr, run, _, advance := stoppedVM(t, func(cfg *Config) {
		cfg.hostMemory = func() (uint64, uint64, error) {
			if short.Load() {
				return 32 << 30, 1 << 20, nil
			}
			return 32 << 30, 24 << 30, nil
		}
	})
	sub := e.subscribe(t)
	short.Store(true)
	advance(cleanupRetryBase)
	e.sched.sweepRetained(t.Context())
	short.Store(false)
	advance(5 * time.Minute)
	if err := e.sched.Release(t.Context(), run.ID, e.member.ID); err == nil || vr.boots.Load() != 2 {
		t.Fatalf("Free sandbox once the host has memory = %v after %d boots, want its boot to fail as the second", err, vr.boots.Load())
	}
	advance(10 * time.Minute)
	e.sched.sweepRetained(t.Context())
	if got := vr.boots.Load(); got != 2 {
		t.Fatalf("%d capture boots, want none while the boot's backoff waits", got)
	}
	for range 10 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	timelineNote(t, sub, run.ID, "could not be captured in 6 tries")
	if got := vr.boots.Load(); got != 7 {
		t.Fatalf("given up after %d capture boots, want 7: the member's and six counted tries that each booted the VM", got)
	}
}

// A session that was open when its sandbox's removal became uncertain has
// its work captured before each retry of that removal. A capture that kept
// failing was tried on every poll, for ever.
func TestAnOpenSessionsFailingCaptureIsGivenUpBeforeItsSandboxRemoval(t *testing.T) {
	t.Parallel()
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) { cfg.Now = clock })
	ctx := t.Context()
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "removal pending",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.UpdateRunStatus(ctx, r.ID, domain.RunProvisioning, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cid, err := e.rt.Create(ctx, runtime.Spec{
		Name: string(r.ID), Image: "busybox:1.36", Command: []string{"fake-agent"},
		TTY: true, CreationKey: string(r.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.rt.Start(ctx, cid); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &destroyRetryRuntime{Runtime: e.rt}
	if err = s2.recoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	cause := errors.New("evidence: retain git state: gitengine: evidence base is unavailable")
	capture.gitErr = func(domain.RunID) error { return cause }
	s2.UseEvidence(capture)
	sub := e.subscribe(t)

	for range 5 {
		s2.sweepRetained(ctx)
	}
	if got := finishCaptures(capture, r.ID); got != 1 {
		t.Fatalf("5 polls at one moment tried %d captures, want one and then its backoff", got)
	}
	for range 10 {
		advance(cleanupRetryMax)
		s2.sweepRetained(ctx)
	}
	if got := finishCaptures(capture, r.ID); got != 6 {
		t.Fatalf("captures tried = %d, want 6 and then no more", got)
	}
	timelineNote(t, sub, r.ID, cause.Error())
	if e.rt.byName(string(r.ID)) != nil {
		t.Fatal("the sandbox stayed although its checkout is on the server and the capture was given up")
	}
	if row := e.waitStoreStatus(t, r.ID, domain.RunInterrupted); row.Status != domain.RunInterrupted {
		t.Fatalf("session after its sandbox was removed = %s, want interrupted", row.Status)
	}
}
