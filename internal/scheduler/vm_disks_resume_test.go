package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// stuckResumeRuntime is diskRuntime whose resumes fail while fail is set.
type stuckResumeRuntime struct {
	*diskRuntime
	fail    atomic.Bool
	resumes atomic.Int32
}

var errStuckResume = errors.New("vm runtime: resume VM: cloud-hypervisor vm.resume: 500 Internal Server Error: the monitor is busy")

func (r *stuckResumeRuntime) Resume(ctx context.Context, id runtime.ID) error {
	r.resumes.Add(1)
	if r.fail.Load() {
		return errStuckResume
	}
	// As the VM runtime does, a resume of a VM that already runs succeeds.
	if c, err := r.get(id); err == nil && containerState(c) == "running" {
		return nil
	}
	return r.diskRuntime.Resume(ctx, id)
}

// heldByTheDiskGuard launches a session, fills the disk from it until the
// guard holds it, then gives the space back with its resume failing.
func heldByTheDiskGuard(t *testing.T) (e *testEnv, rr *stuckResumeRuntime, run domain.RunID, advance func(), check func()) {
	t.Helper()
	clock, move := movableClock()
	var free atomic.Uint64
	free.Store(50 << 30)
	e, _ = newACPEnv(t, func(cfg *Config) {
		rr = &stuckResumeRuntime{diskRuntime: &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}, held: map[runtime.ID]bool{}}}
		cfg.Runtime = rr
		cfg.MinFreeBytes = 10 << 30
		cfg.Now = clock
		cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free.Load()}, nil
		}
	})
	run = e.launchACP(t, "say pong").ID
	waitItems(t, e.sched, run, "the turn", turnEnded("end_turn", 1))
	check = func() { e.sched.guardVMDisks(t.Context()) }
	grow := func(n int64) {
		rr.mu.Lock()
		rr.use[e.cid(t, run)] += n
		rr.mu.Unlock()
	}
	free.Store(5 << 30)
	grow(0)
	check()
	grow(1 << 30)
	check()
	if !e.sched.Paused(run) {
		t.Fatal("the disk guard did not hold the session that filled the disk")
	}
	rr.fail.Store(true)
	free.Store(20 << 30)
	return e, rr, run, func() { move(vmDiskCheck) }, check
}

// With space back the guard resumes what it held. A resume that failed was
// tried again on every check, 5 seconds apart, for ever, and only the server
// log said so.
func TestTheDiskGuardBacksOffAResumeThatFailsAndSaysSo(t *testing.T) {
	t.Parallel()
	e, rr, run, advance, check := heldByTheDiskGuard(t)
	sub := e.subscribe(t)
	for range 20 {
		check()
	}
	if got := rr.resumes.Load(); got != 1 {
		t.Fatalf("20 checks at one moment tried %d resumes, want one", got)
	}
	if note := timelineNote(t, sub, run, errStuckResume.Error()); !strings.Contains(note, "tries again") {
		t.Errorf("the note with the error does not say the guard tries again: %q", note)
	}
	advance()
	check()
	check()
	if got := rr.resumes.Load(); got != 2 {
		t.Fatalf("one check period later: %d resumes, want 2", got)
	}
	advance()
	check()
	if got := rr.resumes.Load(); got != 2 {
		t.Fatalf("one period after the second failure: %d resumes, want the wait doubled", got)
	}
	advance()
	check()
	if got := rr.resumes.Load(); got != 3 {
		t.Fatalf("two periods after the second failure: %d resumes, want 3", got)
	}

	rr.fail.Store(false)
	for range 4 {
		advance()
	}
	check()
	if e.sched.Paused(run) {
		t.Fatal("once its resume worked again, the session stayed paused")
	}
	timelineNote(t, sub, run, diskReleasedNote)
}

// A resume that never works is the member's after the guard's last try: the
// hold ends, the timeline says so, and no check tries again.
func TestTheDiskGuardLeavesASessionItCannotResumeToItsMember(t *testing.T) {
	t.Parallel()
	e, rr, run, advance, check := heldByTheDiskGuard(t)
	sub := e.subscribe(t)
	for range 400 {
		check()
		advance()
	}
	tried := rr.resumes.Load()
	for range 400 {
		check()
		advance()
	}
	if got := rr.resumes.Load(); got != tried || tried != 8 {
		t.Fatalf("resumes tried: %d over 400 check periods, %d over 800; want 8, then no more", tried, got)
	}
	if note := timelineNote(t, sub, run, "Resume it yourself"); !strings.Contains(note, errStuckResume.Error()) {
		t.Errorf("the hand-over note lacks the error: %q", note)
	}
	sc, err := e.sched.readSidecar(run)
	if err != nil || sc.DiskHeld || !sc.Paused {
		t.Fatalf("after the guard's last try: sidecar %+v, %v; want the session paused and no longer the guard's", sc, err)
	}
	rr.fail.Store(false)
	if err = e.sched.Resume(t.Context(), run, e.member.ID); err != nil || e.sched.Paused(run) {
		t.Fatalf("the member's own resume = %v, paused %t", err, e.sched.Paused(run))
	}
}

// A server that stopped between the hypervisor's resume and its own record
// leaves a session recorded as held whose VM runs. The next check records it
// as running.
func TestTheDiskGuardRecordsAHeldSessionWhoseVMAlreadyRuns(t *testing.T) {
	t.Parallel()
	e, rr, run, _, check := heldByTheDiskGuard(t)
	sub := e.subscribe(t)
	if err := rr.diskRuntime.Resume(t.Context(), e.cid(t, run)); err != nil {
		t.Fatal(err)
	}
	rr.fail.Store(false)
	check()
	sc, err := e.sched.readSidecar(run)
	if err != nil || sc.Paused || sc.DiskHeld || e.sched.Paused(run) {
		t.Fatalf("after one check: sidecar %+v, %v, paused %t; want the session recorded as running", sc, err, e.sched.Paused(run))
	}
	timelineNote(t, sub, run, diskReleasedNote)
}

// Resume reports an error after it resumed the session when it could not
// recover the session's terminals. That is a release, not a failed resume.
func TestTheDiskGuardReleasesASessionWhoseTerminalsWereNotRecovered(t *testing.T) {
	t.Parallel()
	e, rr, run, _, check := heldByTheDiskGuard(t)
	sub := e.subscribe(t)
	rr.fail.Store(false)
	e.sched.mu.Lock()
	delete(e.sched.runShellTerminals, run)
	e.sched.mu.Unlock()
	registry := e.sched.runTerminalsPath(run)
	if err := errors.Join(os.MkdirAll(filepath.Dir(registry), 0o700), os.WriteFile(registry, []byte("{"), 0o600)); err != nil {
		t.Fatal(err)
	}
	check()
	if e.sched.Paused(run) {
		t.Fatal("the session stayed paused")
	}
	timelineNote(t, sub, run, diskReleasedNote)
	e.sched.mu.Lock()
	released, retries := e.sched.runs[run].diskReleasedAt, e.sched.runs[run].diskRetries
	e.sched.mu.Unlock()
	if released.IsZero() || retries != 0 {
		t.Fatalf("released at %v after %d failed resumes; want the release recorded and no failed resume", released, retries)
	}
}
