package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// The admissions the capacity counters tell apart.
const (
	opLaunch      = "launch"
	opLaunchRetry = "launch-retry"
	opWake        = "wake"
	opBoot        = "boot"
	opStart       = "start"
)

// idleHour is how long a session VM has been idle before the memory it
// holds counts as idle in a refusal's figures.
const idleHour = time.Hour

// countCapacity logs e and adds it to its counter. A count that cannot be
// written is logged: what was counted has already happened.
func (s *Scheduler) countCapacity(e domain.CapacityEvent) {
	e.At = time.Now().UTC()
	slog.Info("scheduler: capacity: "+e.Kind, "op", e.Op, "cause", e.Cause, "run", e.Run, "available", e.AvailableBytes, "floor", e.FloorBytes,
		"used", e.UsedBytes, "idle_hour", e.IdleHourBytes, "vm", e.VMBytes, "text", e.Text)
	ctx, cancel := context.WithTimeout(context.Background(), capacityProbeTimeout)
	defer cancel()
	if err := s.cfg.Store.CountCapacityEvent(ctx, e); err != nil {
		slog.Warn("scheduler: capacity: count "+e.Kind, "error", err)
	}
}

// countRefusal counts the admission of claim that err refused. A memory
// refusal carries the host memory in use and what session VMs idle for
// idleHour held of it.
func (s *Scheduler) countRefusal(ctx context.Context, claim *memoryClaim, err error) {
	e := domain.CapacityEvent{Kind: domain.CapacityRefusal, Op: claim.op, Run: claim.run}
	var short *memoryShortage
	switch {
	case err == nil || ctx.Err() != nil:
		return
	case errors.As(err, &short):
		e.Cause, e.AvailableBytes, e.FloorBytes = "memory", short.available, short.floor
	case errors.Is(err, ErrMemoryPressure):
		e.Cause = "stall"
	case errors.Is(err, ErrDiskFull):
		e.Cause = "disk"
	case errors.Is(err, ErrCapacityUnknown):
		e.Cause = "unknown"
	default:
		return
	}
	e.Text = err.Error()
	if e.Cause == "memory" || e.Cause == "stall" {
		if total, available, memErr := s.hostMemoryTotal(); memErr == nil {
			e.UsedBytes = total - available
		}
		candidates, _ := s.memoryCandidates(ctx)
		now := time.Now()
		for _, c := range candidates {
			if c.idle && now.Sub(c.since) >= idleHour {
				e.IdleHourBytes += c.memory
			}
		}
	}
	s.countCapacity(e)
}

// countSave counts the save of c's VM for want of memory, with the host
// memory measured before it.
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
	s.countCapacity(domain.CapacityEvent{Kind: domain.CapacitySave, Op: op, Run: c.e.runID, AvailableBytes: available, FloorBytes: floor, VMBytes: c.memory})
}
