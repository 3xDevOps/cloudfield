package scheduler

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// ladderRuntime counts the passes of the reclaim ladder below the reserve:
// each drops the kept checkout disks once.
type ladderRuntime struct {
	*saveRuntime
	passes atomic.Int32
}

func (r *ladderRuntime) SweepImages() error { return nil }

func (r *ladderRuntime) DropKeptCheckoutDisks() error {
	r.passes.Add(1)
	return nil
}

// Below the reserve with nothing left to free, as when every sleeping
// session's agent cannot start again from its disk, each refused wake used
// to run the whole ladder. One pass answers them all, until what the ladder
// reads of the sessions changes or its period has passed.
func TestRefusedWakesBelowTheReserveShareOneReclaimPass(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	clock, advance := movableClock()
	e.sched.cfg.Now = clock
	lr := &ladderRuntime{saveRuntime: sr}
	e.sched.cfg.Runtime = lr

	open := e.launchACP(t, "say pong").ID
	waitItems(t, e.sched, open, "the open session's turn", turnEnded("end_turn", 1))
	var asleep []domain.RunID
	for range 3 {
		run := e.launchACP(t, "say pong").ID
		waitItems(t, e.sched, run, "the task's turn", turnEnded("end_turn", 1))
		e.saveRun(t, run)
		asleep = append(asleep, run)
	}
	var free atomic.Uint64
	free.Store(512 << 20)
	setFreeBytes(e, &free)
	wake := func(run domain.RunID) {
		t.Helper()
		if _, err := e.sched.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: "after sleep"}, false, nil); !errors.Is(err, ErrDiskFull) {
			t.Errorf("a wake with 512 MiB free = %v, want %v", err, ErrDiskFull)
		}
	}

	var wg sync.WaitGroup
	for _, run := range asleep {
		wg.Go(func() { wake(run) })
	}
	wg.Wait()
	for range 5 {
		for _, run := range asleep {
			wake(run)
		}
	}
	if got := lr.passes.Load(); got != 1 {
		t.Fatalf("18 refused wakes with nothing to free ran the reclaim ladder %d times, want once", got)
	}

	// A session closes: its sandbox is something a pass can free.
	if err := e.sched.CloseRun(t.Context(), open, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	wake(asleep[0])
	if lr.passes.Load() < 2 || e.rt.byName(string(open)) != nil {
		t.Fatalf("after a session closed, a refused wake ran %d passes and left its sandbox %t; want another pass that removes it",
			lr.passes.Load(), e.rt.byName(string(open)) != nil)
	}
	wake(asleep[1])
	settled := lr.passes.Load()
	for range 5 {
		wake(asleep[2])
	}
	if got := lr.passes.Load(); got != settled {
		t.Fatalf("once nothing was left again, 5 refused wakes ran %d more passes", got-settled)
	}

	advance(reclaimSweepEvery)
	wake(asleep[0])
	wake(asleep[1])
	if got := lr.passes.Load(); got != settled+1 {
		t.Fatalf("%v later, 2 refused wakes ran %d passes, want one", reclaimSweepEvery, got-settled)
	}
}
