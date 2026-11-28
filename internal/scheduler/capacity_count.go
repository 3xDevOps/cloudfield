package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// The admissions the capacity counters tell apart.
const (
	opLaunch      = "launch"
	opLaunchRetry = "launch-retry"
	opWake        = "wake"
	opWakeAuto    = "wake-auto"
	opBoot        = "boot"
	opStart       = "start"
)

// autoWake marks the context of a wake nobody asked for: the memory guard
// waking a VM it saved while it worked, and the sweep waking a session idle
// for days to close it. Their refusals are counted apart.
type autoWake struct{}

// idleHour is how long a session VM has been idle before the memory it
// holds counts as idle in a refusal's figures.
const idleHour = time.Hour

// capacityCount is an event on its way to the counters; memory asks for
// the memory in use and what idle session VMs hold of it.
type capacityCount struct {
	event  domain.CapacityEvent
	memory bool
}

// capacityCountsKept is how long a counter's hour is kept. A server start
// deletes older ones.
const capacityCountsKept = 90 * 24 * time.Hour

// vmCountEvery is how often the session VMs are counted for
// cloudfield-server status. A count is written when it differs from the
// last and at least every vmCountKept, so the time on it says a server was
// running then.
const (
	vmCountEvery = 5 * time.Second
	vmCountKept  = time.Minute
)

// capacityCountBacklog is how many events may wait for the database. More
// than that at once are dropped with a warning: counting never holds up
// what it counts.
const capacityCountBacklog = 256

// countCapacity hands e to countCapacityEvents and returns at once.
func (s *Scheduler) countCapacity(e domain.CapacityEvent, memory bool) {
	e.At = time.Now().UTC()
	select {
	case s.capacityCounts <- capacityCount{event: e, memory: memory}:
	default:
		slog.Warn("scheduler: capacity: not counted, too many wait for the database", "kind", e.Kind, "op", e.Op, "cause", e.Cause, "run", e.Run, "text", e.Text)
	}
}

// countCapacityEvents logs each event and adds it to its counter until ctx
// ends. A count that cannot be written is logged: what was counted has
// already happened. The memory figures of a refusal are measured here, a
// moment after it, and cover the session VMs the memory guard may save.
// counts is the store the scheduler was built with.
func (s *Scheduler) countCapacityEvents(ctx context.Context, counts store.CapacityStore) {
	if err := counts.PruneCapacityCounts(ctx, time.Now().Add(-capacityCountsKept)); err != nil {
		slog.Warn("scheduler: capacity: prune old counts", "error", err)
	}
	// recorded is the VM count in the store, for cloudfield-server status,
	// and when it was written; nil until this server wrote one, whatever
	// an earlier one left.
	var recorded *domain.VMCounts
	var recordedAt time.Time
	vms := time.NewTicker(vmCountEvery)
	defer vms.Stop()
	for {
		var c capacityCount
		select {
		case <-ctx.Done():
			return
		case now := <-vms.C:
			if counted := s.VMs(); recorded == nil || counted != *recorded || now.Sub(recordedAt) >= vmCountKept {
				if err := counts.RecordVMCounts(ctx, counted, now); err != nil {
					slog.Warn("scheduler: capacity: record the session VM counts", "error", err)
				}
				// A failed write is tried again with the next one due, not
				// every tick.
				recorded, recordedAt = &counted, now
			}
			continue
		case c = <-s.capacityCounts:
		}
		if ctx.Err() != nil {
			slog.Warn("scheduler: capacity: not counted, the server is stopping", "kind", c.event.Kind, "op", c.event.Op, "cause", c.event.Cause, "run", c.event.Run, "text", c.event.Text)
			continue
		}
		e := c.event
		if c.memory {
			if total, available, err := s.hostMemoryTotal(); err == nil {
				e.UsedBytes = total - available
			}
			candidates, _ := s.memoryCandidates(ctx)
			for _, candidate := range candidates {
				// A closed session's VM has no time of last use and is idle.
				if candidate.closed || candidate.idle && e.At.Sub(candidate.since) >= idleHour {
					e.IdleHourBytes += candidate.memory
				}
			}
		}
		slog.Info("scheduler: capacity: "+e.Kind, "op", e.Op, "cause", e.Cause, "run", e.Run, "available", e.AvailableBytes, "floor", e.FloorBytes,
			"used", e.UsedBytes, "idle_hour", e.IdleHourBytes, "vm", e.VMBytes, "text", e.Text)
		writeCtx, cancel := context.WithTimeout(ctx, capacityProbeTimeout)
		err := counts.CountCapacityEvent(writeCtx, e)
		cancel()
		if err != nil {
			slog.Warn("scheduler: capacity: count "+e.Kind, "error", err)
		}
	}
}

// countRefusal counts the admission of claim that err refused. A refusal
// that only says its caller gave up is not one.
func (s *Scheduler) countRefusal(ctx context.Context, claim *memoryClaim, err error) {
	e := domain.CapacityEvent{Kind: domain.CapacityRefusal, Op: claim.op, Run: claim.run}
	var short *memoryShortage
	switch {
	case err == nil:
		return
	case errors.As(err, &short):
		e.Cause, e.AvailableBytes, e.FloorBytes = "memory", short.available, short.floor
	case errors.Is(err, ErrMemoryPressure):
		e.Cause = "stall"
	case errors.Is(err, ErrDiskFull):
		e.Cause = "disk"
	case errors.Is(err, ErrCapacityUnknown) && ctx.Err() == nil:
		e.Cause = "unknown"
	default:
		return
	}
	e.Text = err.Error()
	s.countCapacity(e, e.Cause == "memory" || e.Cause == "stall")
}

// countSave counts the save of c's VM for want of memory, or the stop of a
// closed session's, with the host memory measured before it.
func (s *Scheduler) countSave(c memoryCandidate, available, floor uint64) {
	op := "working"
	switch {
	case c.closed:
		op = "closed"
	case c.resident:
		op = "paused"
	case c.idle:
		op = "idle"
	}
	s.countCapacity(domain.CapacityEvent{Kind: domain.CapacitySave, Op: op, Run: c.e.runID, AvailableBytes: available, FloorBytes: floor, VMBytes: c.memory}, false)
}
