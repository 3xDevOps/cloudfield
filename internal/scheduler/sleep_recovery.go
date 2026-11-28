package scheduler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// recoverAsleep adopts an open run whose sidecar says it is asleep, and
// reports false when its container is gone, which the caller handles as
// for any run.
func (s *Scheduler) recoverAsleep(ctx context.Context, r *domain.Run, sc sidecar) bool {
	info, err := s.cfg.Runtime.Inspect(ctx, runtime.ID(sc.ContainerID))
	if errors.Is(err, runtime.ErrNotFound) {
		return false
	}
	// Installed without waiting on or attaching to the container.
	var entry *supervised
	s.mu.Lock()
	if s.runs[r.ID] == nil && s.pending[r.ID] == nil {
		entry = s.entryFromSidecar(r, sc)
		entry.paused = true
		s.runs[r.ID] = entry
		s.syncRunUserReservationsLocked()
	}
	s.mu.Unlock()
	// Started by hand while the server was down, or a VM whose save the
	// restart interrupted: this process holds none of its streams. A VM the
	// memory guard marked to wake, one it was saving or the brake left
	// frozen, is the guard's to save frozen or wake: a save here would let
	// it run first.
	if entry != nil && err == nil && !info.Exited && sc.MemorySaved == 0 {
		s.stopRecoveredAsleep(ctx, entry)
	}
	return true
}

// stopRecoveredAsleep stops the running container of a run recovery adopted
// asleep, or saves an open session's VM, as it stands if a server left it
// frozen: the brake thaws a VM whose lifecycle lock is held. A VM whose
// memory does not fit on the data disk, as saveFits rules, stays as it is,
// for the memory guard to save or a wake to resume, as does one whose save
// fails. A wake or Relaunch that won the lifecycle lock first and started
// the container on purpose keeps it running.
func (s *Scheduler) stopRecoveredAsleep(ctx context.Context, entry *supervised) {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	frozen := s.vmFrozen(cid)
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	asleep, closed := s.runs[entry.runID] == entry && !entry.asleepSince.IsZero(), entry.retained
	s.mu.Unlock()
	if !asleep {
		return
	}
	if saver, ok := s.entrySaver(entry); !closed && (frozen || ok) {
		err := errors.New("its memory is unknown or does not fit on the data disk; the memory guard saves it once it fits and the host is short of memory, and a wake resumes it")
		if saving, fits := s.recoveredSaveFits(ctx, entry); fits {
			if frozen {
				err = s.saveAsleepRunning(ctx, entry)
			} else {
				err = s.saveRecoveredAsleep(ctx, entry, saver)
			}
			saving()
		}
		if err != nil {
			slog.Warn("scheduler: save a sleeping session's VM; it stays running", "run", entry.runID, "error", err)
		}
		s.mu.Lock()
		entry.asleepRunning = !entry.asleepSaved
		s.mu.Unlock()
		return
	}
	if err := s.stopLeftoverAsleep(ctx, entry); err != nil {
		slog.Warn("scheduler: stop a sleeping session's container", "run", entry.runID, "error", err)
	}
}

// stopLeftoverAsleep finishes a sleep a crash interrupted: it removes the
// browser companion and stops the container. Caller holds
// entry.lifecycleMu.
func (s *Scheduler) stopLeftoverAsleep(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	cid := entry.containerID
	entry.sleepGen++
	s.mu.Unlock()
	// A companion left behind holds no agent: the container still stops,
	// and the next Browser call removes the companion or reports why not.
	if err := s.removeDevelopmentBrowser(ctx, entry.runID); err != nil {
		s.mu.Lock()
		entry.browserLeft = true
		s.mu.Unlock()
	}
	return s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace)
}
