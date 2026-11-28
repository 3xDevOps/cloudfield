package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// counted returns the counter of kind, op and cause over the last hours,
// once it holds one event.
func (e *testEnv) counted(t *testing.T, kind, op, cause string) domain.CapacityCount {
	t.Helper()
	var found domain.CapacityCount
	waitFor(t, "the "+kind+" "+op+" "+cause+" counter", func() bool {
		counts, err := e.db.CapacityCounts(t.Context(), time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		found = domain.CapacityCount{}
		for _, c := range counts {
			if c.Kind == kind && c.Op == op && c.Cause == cause {
				found.Count += c.Count
				found.AvailableBytes += c.AvailableBytes
				found.FloorBytes += c.FloorBytes
				found.UsedBytes += c.UsedBytes
				found.IdleHourBytes += c.IdleHourBytes
				found.VMBytes += c.VMBytes
				found.LastRun, found.LastText = c.LastRun, c.LastText
			}
		}
		return found.Count == 1
	})
	return found
}

// A memory refusal is counted with the memory in use and what session VMs
// idle for over an hour held of it.
func TestAMemoryRefusalIsCountedWithWhatIdleSessionsHeld(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	old := e.launchVM(t, mr, "say pong", 2<<30)
	e.launchVM(t, mr, "say pong", 1<<30)
	e.launchVM(t, mr, acpmock.PromptWait, 4<<30)
	oldRun := e.runOf(t, old)
	e.sched.mu.Lock()
	asleep := e.sched.runs[oldRun]
	e.sched.mu.Unlock()
	// Recorded asleep and still running, as a restart takes a VM up.
	if err := e.sched.markAsleep(asleep); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	asleep.asleepSince = time.Now().UTC().Add(-2 * idleHour)
	e.sched.mu.Unlock()

	// Too little for saving every idle VM to admit a start, so none is saved.
	mr.setAvailable(100 << 20)
	_, err := e.sched.reserveLaunch(t.Context(), "run-reserved", false)
	if !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a launch with 100 MiB available = %v, want the memory refusal", err)
	}
	got := e.counted(t, domain.CapacityRefusal, opLaunch, "memory")
	want := domain.CapacityCount{Count: 1, AvailableBytes: 100 << 20, FloorBytes: memoryFloor(32 << 30), UsedBytes: 32<<30 - 100<<20,
		IdleHourBytes: 2 << 30, LastRun: "run-reserved", LastText: err.Error()}
	if got != want {
		t.Fatalf("counted refusal = %+v\nwant %+v", got, want)
	}
	if _, err = e.sched.reserveLaunch(t.Context(), "run-reserved", true); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("the launch tried again = %v, want the memory refusal", err)
	}
	e.counted(t, domain.CapacityRefusal, opLaunchRetry, "memory")
	e.counted(t, domain.CapacityRefusal, opLaunch, "memory")
}

// A save by the memory guard and the refused wake of what it saved are
// counted, the wake with its session.
func TestAGuardSaveAndARefusedWakeAreCounted(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	vm := e.launchVM(t, mr, "say pong", 6<<30)
	run := e.runOf(t, vm)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(vm) {
		t.Fatal("the guard did not save the idle VM")
	}
	saved := e.counted(t, domain.CapacitySave, "idle", "")
	if saved.VMBytes != 6<<30 || saved.AvailableBytes != 3<<30 || saved.FloorBytes != memoryFloor(32<<30) || saved.LastRun != run {
		t.Fatalf("counted save = %+v, want the VM's 6 GiB, the 3 GiB available before it and its session", saved)
	}

	mr.setAvailable(memoryFloor(32<<30) + 6<<30 - 1)
	err := e.sched.WakeRun(t.Context(), run, "")
	if !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a wake one byte short = %v, want the memory refusal", err)
	}
	if refused := e.counted(t, domain.CapacityRefusal, opWake, "memory"); refused.LastRun != run {
		t.Fatalf("counted wake refusal = %+v, want it to name the session", refused)
	}
	// A wake the guard tries by itself is counted apart from one asked for.
	if err = e.sched.WakeRun(context.WithValue(t.Context(), autoWake{}, true), run, ""); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("the guard's own wake one byte short = %v, want the memory refusal", err)
	}
	e.counted(t, domain.CapacityRefusal, opWakeAuto, "memory")
	e.counted(t, domain.CapacityRefusal, opWake, "memory")
}

// The brake freezing the session VMs is counted once per shortage.
func TestAFreezeIsCountedOncePerShortage(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 4<<30)
	floor := memoryFloor(32 << 30)
	e.brake(fr, 4*floor)
	e.brake(fr, floor-1)
	e.brake(fr, floor-2)
	if froze := e.counted(t, domain.CapacityFreeze, "", ""); froze.AvailableBytes != floor-1 || froze.FloorBytes != floor {
		t.Fatalf("counted freeze = %+v, want one, with the memory that caused it", froze)
	}
}

// The session VMs are counted by what they hold of the host, and the count
// is kept where cloudfield-server status reads it.
func TestSessionVMsAreCountedByWhatTheyHold(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	mr := fr.memoryRuntime
	paused := e.launchVM(t, mr, "say pong", 1<<30)
	saved := e.launchVM(t, mr, "say pong", 6<<30)
	e.launchVM(t, mr, acpmock.PromptWait, 1<<30)
	e.launchVM(t, mr, acpmock.PromptWait, 1<<30)
	if got, want := e.sched.VMs(), (domain.VMCounts{Awake: 4}); got != want {
		t.Fatalf("four running VMs counted as %+v", got)
	}
	// The server writes its count where cloudfield-server status reads it.
	waitFor(t, "the VM count in the store", func() bool {
		got, at, err := e.db.VMCounts(t.Context())
		return err == nil && !at.IsZero() && got.Awake == 4
	})
	stopped := e.launchVM(t, mr, "say pong", 1<<30)
	pausedRun, savedRun, stoppedRun := e.runOf(t, paused), e.runOf(t, saved), e.runOf(t, stopped)
	e.sched.mu.Lock()
	asleep := []*supervised{e.sched.runs[pausedRun], e.sched.runs[savedRun], e.sched.runs[stoppedRun]}
	e.sched.mu.Unlock()
	for _, entry := range asleep {
		if err := e.sched.markAsleep(entry); err != nil {
			t.Fatal(err)
		}
	}
	e.sched.mu.Lock()
	// As a restart leaves a VM it did not save, as one it saved, and as
	// one whose saved memory was reclaimed: nothing is kept.
	asleep[0].asleepRunning, asleep[1].asleepSaved = true, true
	e.sched.mu.Unlock()
	floor := memoryFloor(32 << 30)
	e.brake(fr, 4*floor)
	e.brake(fr, floor-1)
	if got, want := e.sched.VMs(), (domain.VMCounts{Frozen: 2, Paused: 1, Saved: 1, Stopped: 1}); got != want {
		t.Fatalf("VMs = %+v, want %+v", got, want)
	}
	// A closed session's VM still running is counted as that.
	e.sched.mu.Lock()
	since := asleep[2].asleepSince
	asleep[2].retained, asleep[2].asleepSince = true, time.Time{}
	e.sched.mu.Unlock()
	if got, want := e.sched.VMs(), (domain.VMCounts{Frozen: 2, Paused: 1, Saved: 1, Closed: 1}); got != want {
		t.Fatalf("VMs with a closed session's VM in memory = %+v, want %+v", got, want)
	}
	e.sched.mu.Lock()
	asleep[2].retained, asleep[2].asleepSince = false, since
	e.sched.mu.Unlock()
	m := e.sched.Machine(t.Context())
	if m.MemoryAvailableBytes != floor-1 || m.MemoryFloorBytes != floor || m.MemoryTotalBytes != 32<<30 || m.StartMemoryBytes != startupMemoryBytes {
		t.Fatalf("machine memory = %+v, want what admission reads", m)
	}
}
