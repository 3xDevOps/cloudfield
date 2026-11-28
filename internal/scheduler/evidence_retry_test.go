package scheduler

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/evidence"
)

// vmDiskGit is fakeGit with every checkout on a disk of its session's VM,
// which the server never reads.
type vmDiskGit struct{ *fakeGit }

func (vmDiskGit) HostCheckout(domain.RunID) bool { return false }

// finishCaptures counts the tries at the capture run's sandbox is kept for,
// not the one checkout cleanup makes before it removes a checkout.
func finishCaptures(capture *schedulerEvidenceCapture, run domain.RunID) int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	n := 0
	for _, req := range capture.reqs {
		if req.RunID == run && strings.HasPrefix(req.IdempotencyKey, "finish:") {
			n++
		}
	}
	return n
}

// timelineNote returns the first note published for run that contains want.
func timelineNote(t *testing.T, sub events.Subscription, run domain.RunID, want string) string {
	t.Helper()
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.TimelinePayload); ok && ev.RunID == run && p.Kind == events.TimelineNote && strings.Contains(p.Message, want) {
				return p.Message
			}
		case <-time.After(waitTimeout):
			t.Fatalf("no timeline note for %s containing %q", run, want)
		}
	}
}

// A closed session whose guest disk went read-only can never be captured.
// Every poll and every reclaim pass used to try again, for ever, and the VM
// could never be removed, not even by deleting the session.
func TestACaptureThatNeverSucceedsIsGivenUpAndSaidSoOnTheSession(t *testing.T) {
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.Now = clock
		cfg.Git = vmDiskGit{cfg.Git.(*fakeGit)}
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	stuck, _ := e.launchFake(t, "guest disk went read-only")
	cause := errors.New("evidence: retain git state: gitengine: capture evidence tree for session " + string(stuck.ID) + ": guest: stage: read-only file system")
	capture.gitErr = func(run domain.RunID) error {
		if run == stuck.ID {
			return cause
		}
		return nil
	}
	sub := e.subscribe(t)
	if err := e.sched.CloseRun(t.Context(), stuck.ID, e.member.ID, domain.RunMerged); !errors.Is(err, cause) {
		t.Fatalf("close of a session whose capture fails = %v, want %v", err, cause)
	}
	behind := closeRetained(t, e, "closed after it")

	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	if e.rt.byName(string(behind.ID)) != nil {
		t.Fatal("the session whose capture fails kept reclaim from the closed session behind it")
	}
	if e.rt.byName(string(stuck.ID)) == nil {
		t.Fatal("reclaim removed a VM whose work was never captured")
	}

	sweep := func(times int) {
		for range times {
			advance(cleanupRetryMax)
			e.sched.sweepRetained(t.Context())
			e.sched.reclaimDisk(t.Context(), "")
		}
	}
	sweep(10)
	tried := finishCaptures(capture, stuck.ID)
	sweep(30)
	if got := finishCaptures(capture, stuck.ID); got != tried || tried != 6 {
		t.Fatalf("captures tried: %d after 10 polls, %d after 40; want 6, then no more", tried, got)
	}
	if note := timelineNote(t, sub, stuck.ID, cause.Error()); !strings.Contains(note, "Deleting the session tries the capture once more") ||
		!strings.Contains(note, "too short of memory to boot the VM for that try, the delete is refused") {
		t.Errorf("the note with the error does not say how to remove the sandbox, or when a delete is refused: %q", note)
	}
	row, err := e.db.GetRun(t.Context(), stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.sched.Retention(row)
	if err != nil || info.CleanupError != cleanupKeptError {
		t.Errorf("retention of the session given up on = %+v, %v; want cleanup error %q", info, err, cleanupKeptError)
	}
	if e.rt.byName(string(stuck.ID)) == nil {
		t.Fatal("giving up removed a VM whose work was never captured")
	}

	if err = e.sched.Release(t.Context(), stuck.ID, e.member.ID); !errors.Is(err, cause) {
		t.Errorf("freeing the sandbox = %v, want the capture's error %v", err, cause)
	}
	if _, err = e.sched.Relaunch(t.Context(), stuck.ID, e.member.ID); err == nil || e.rt.byName(string(stuck.ID)) == nil {
		t.Errorf("relaunch of the session given up on = %v, VM kept %t; want it refused and the VM kept", err, e.rt.byName(string(stuck.ID)) != nil)
	}
	tried = finishCaptures(capture, stuck.ID)
	if err = e.sched.DeleteRun(t.Context(), stuck.ID, e.member.ID); err != nil {
		t.Fatalf("delete of the session given up on: %v", err)
	}
	if e.rt.byName(string(stuck.ID)) != nil || finishCaptures(capture, stuck.ID) != tried+1 {
		t.Errorf("delete left the VM %t and tried %d more captures; want it removed after one last try",
			e.rt.byName(string(stuck.ID)) != nil, finishCaptures(capture, stuck.ID)-tried)
	}
}

// givenUpVM closes a session whose checkout is on its VM's disk and whose
// capture always fails with cause, and polls until the capture is given up.
func givenUpVM(t *testing.T, mutate func(*Config)) (*testEnv, *schedulerEvidenceCapture, *domain.Run, func(time.Duration)) {
	t.Helper()
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.Now = clock
		cfg.Git = vmDiskGit{cfg.Git.(*fakeGit)}
		if mutate != nil {
			mutate(cfg)
		}
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	cause := errors.New("evidence: retain git state: gitengine: capture evidence tree: guest: stage: read-only file system")
	capture.gitErr = func(domain.RunID) error { return cause }
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "guest disk went read-only")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); !errors.Is(err, cause) {
		t.Fatalf("close of a session whose capture fails = %v, want %v", err, cause)
	}
	for range 10 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	if got := finishCaptures(capture, run.ID); got != 6 {
		t.Fatalf("captures tried = %d, want 6", got)
	}
	return e, capture, run, advance
}

// A delete whose sandbox removal failed once went back through the capture
// that was given up, so the session could never be deleted. The delete's
// one last try at that capture is not repeated either.
func TestDeletingAKeptSessionSurvivesAFailedSandboxRemoval(t *testing.T) {
	e, capture, run, _ := givenUpVM(t, func(cfg *Config) {
		cfg.Runtime = &destroyRetryRuntime{Runtime: cfg.Runtime}
	})
	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err == nil || !strings.Contains(err.Error(), "destroy temporarily unavailable") {
		t.Fatalf("delete while the sandbox cannot be removed = %v, want the removal's error", err)
	}
	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("delete once the sandbox can be removed: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil || finishCaptures(capture, run.ID) != 7 {
		t.Errorf("delete left the VM %t after %d captures; want it removed after the first delete's one last try",
			e.rt.byName(string(run.ID)) != nil, finishCaptures(capture, run.ID))
	}
}

// What was given up stays given up across a restart: the next server tries
// once as it starts, not six times more, and still says the sandbox is kept.
func TestAGivenUpCaptureStaysGivenUpAcrossARestart(t *testing.T) {
	e, capture, run, advance := givenUpVM(t, nil)
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := e.newScheduler(t, e.rt, newFakePTY())
	restarted.UseEvidence(capture)
	if err := restarted.recoverRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	var free atomic.Uint64
	free.Store(1 << 30)
	restarted.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free.Load()}, nil
	}
	for range 10 {
		advance(cleanupRetryMax)
		restarted.sweepRetained(t.Context())
		restarted.reclaimDisk(t.Context(), "")
	}
	if got := finishCaptures(capture, run.ID); got != 7 {
		t.Fatalf("captures tried = %d after a restart, want the 6 from before it and one as the server started", got)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := restarted.Retention(row)
	if err != nil || info.CleanupError != cleanupKeptError || e.rt.byName(string(run.ID)) == nil {
		t.Fatalf("after a restart: retention %+v, %v, VM kept %t; want cleanup error %q and the VM kept",
			info, err, e.rt.byName(string(run.ID)) != nil, cleanupKeptError)
	}
}

// A sandbox whose capture was waived, and whose removal then failed once,
// went back to a capture on every poll and was never removed.
func TestAWaivedCaptureIsNotTriedAgainAfterAFailedSandboxRemoval(t *testing.T) {
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.Now = clock
		cfg.Runtime = &destroyRetryRuntime{Runtime: cfg.Runtime}
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	cause := errors.New("evidence: retain git state: gitengine: evidence base is unavailable")
	capture.gitErr = func(domain.RunID) error { return cause }
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "host checkout")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); !errors.Is(err, cause) {
		t.Fatalf("close of a session whose capture fails = %v, want %v", err, cause)
	}
	for range 10 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	over := time.Now().UTC().Add(-time.Minute)
	e.sched.mu.Lock()
	e.sched.runs[run.ID].retainedUntil = &over
	e.sched.mu.Unlock()
	for range 3 {
		e.sched.sweepRetained(t.Context())
	}
	if e.rt.byName(string(run.ID)) != nil || finishCaptures(capture, run.ID) != 6 {
		t.Fatalf("after its retention: sandbox left %t, %d captures; want it removed on the poll after the failed removal, with 6 captures",
			e.rt.byName(string(run.ID)) != nil, finishCaptures(capture, run.ID))
	}
}

// A checkout that is a directory on the server outlives the sandbox, and
// checkout cleanup captures it before removing it. Once the capture is given
// up, the sandbox no longer waits for it.
func TestAGivenUpCaptureDoesNotKeepASandboxWhoseCheckoutIsOnTheServer(t *testing.T) {
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.Now = clock
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	cause := errors.New("evidence: retain git state: gitengine: evidence base is unavailable")
	capture.gitErr = func(domain.RunID) error { return cause }
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "host checkout")
	sub := e.subscribe(t)
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); !errors.Is(err, cause) {
		t.Fatalf("close of a session whose capture fails = %v, want %v", err, cause)
	}
	for range 10 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	if got := finishCaptures(capture, run.ID); got != 6 {
		t.Fatalf("captures tried = %d, want 6", got)
	}
	timelineNote(t, sub, run.ID, cause.Error())
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("giving up the capture removed a sandbox still inside its retention")
	}

	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	if e.rt.byName(string(run.ID)) != nil || finishCaptures(capture, run.ID) != 6 {
		t.Fatalf("reclaim left the sandbox %t after %d captures; want it removed with no further try",
			e.rt.byName(string(run.ID)) != nil, finishCaptures(capture, run.ID))
	}
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Worktree == "" {
		t.Fatalf("the checkout that holds the work = %+v, %v; want it kept", row, err)
	}
}

// A host with no memory to boot a stopped VM for its capture gets it back:
// that wait is not a failed try, however long it lasts.
func TestACaptureWaitingForHostMemoryIsNotGivenUp(t *testing.T) {
	clock, advance := movableClock()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
		cfg.Now = clock
		cfg.Git = vmDiskGit{cfg.Git.(*fakeGit)}
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	short := atomic.Bool{}
	short.Store(true)
	capture.gitErr = func(domain.RunID) error {
		if short.Load() {
			return &memoryShortage{available: 1 << 30, floor: 2 << 30, need: 1 << 30}
		}
		return nil
	}
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "stopped before its commit")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("close while the host is short of memory = %v, want %v", err, ErrMemoryPressure)
	}
	e.sched.sweepRetained(t.Context())
	if got := finishCaptures(capture, run.ID); got != 1 {
		t.Fatalf("a poll right after the refused capture tried %d captures, want none before %v", got-1, cleanupRetryBase)
	}
	for range 20 {
		advance(cleanupRetryBase)
		e.sched.sweepRetained(t.Context())
	}
	if got := finishCaptures(capture, run.ID); got != 21 {
		t.Fatalf("captures tried while the host stayed short = %d, want one each %v", got, cleanupRetryBase)
	}
	short.Store(false)
	advance(cleanupRetryBase)
	e.sched.sweepRetained(t.Context())
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("with memory back, the session's sandbox was not captured and removed")
	}
}

// A closed session's sandbox and its finish evidence are both kept 30 days,
// so the capture that the end of retention repeats finds its packet expired.
func TestRetentionEndsThoughTheFinishEvidenceHasExpired(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run := closeRetained(t, e, "kept its whole retention")
	capture.mu.Lock()
	capture.refusal = evidence.ErrExpired
	capture.mu.Unlock()
	over := time.Now().UTC().Add(-time.Minute)
	e.sched.mu.Lock()
	e.sched.runs[run.ID].retainedUntil = &over
	e.sched.mu.Unlock()
	e.sched.sweepRetained(t.Context())
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("a sandbox whose evidence was captured and has expired outlived its retention")
	}
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Reason != retainedExpiredReason {
		t.Fatalf("session after its retention = %+v, %v; want reason %q", row, err, retainedExpiredReason)
	}
}
