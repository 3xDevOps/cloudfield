package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// claimSpare makes launch out of its member's spare when the spare's
// sandbox is the one a cold launch would start now, and returns the run.
// It returns nil, nil for a launch that goes on cold: there is no ready
// spare, or it differs, and is then removed. Until the spare's row is
// claimed nothing of it is visible, so every refusal before that is a cold
// launch; after it the launch is this run's, whatever happens.
func (s *Scheduler) claimSpare(ctx context.Context, launch *domain.Run, ws *domain.Workspace, actor *domain.Member, profile harness.Profile, opts domain.LaunchOptions) (*domain.Run, error) {
	sp := s.takeSpare(launch.MemberID)
	if sp == nil {
		return nil, nil
	}
	began := time.Now()
	run, entry, plan, differs := s.spareFor(ctx, sp, launch, ws, actor, profile)
	if differs == "" && !entry.lifecycleMu.TryLock() {
		differs = "its session is busy"
	}
	if differs != "" {
		slog.Info("scheduler: launch did not claim the spare session", "member", sp.member, "run", sp.run, "reason", differs)
		s.goRemoveSpare(sp)
		return nil, nil
	}
	defer entry.lifecycleMu.Unlock()
	spareBranch := run.Branch
	branch, err := s.cfg.Git.RenameRunBranch(ctx, run.ID, run.Task)
	if err == nil {
		run.Branch = branch
		err = s.cfg.Store.ClaimSpareRun(ctx, run)
	}
	if err != nil {
		slog.Warn("scheduler: launch could not claim the spare session", "member", sp.member, "run", sp.run, "error", err)
		s.goRemoveSpare(sp)
		return nil, nil
	}
	s.cfg.Bus.(runHider).Show(run.ID)
	run.Spare = false
	if opts.Created != nil {
		opts.Created(run.ID)
	}
	if opts.Admitted != nil {
		opts.Admitted()
	}
	s.mu.Lock()
	entry.spare, entry.task, entry.status = nil, run.Task, domain.RunQueued
	err = errors.Join(s.writeSidecar(entry.sidecar()),
		s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunQueued, domain.RunProvisioning, "", actor.ID))
	cid := entry.containerID
	s.mu.Unlock()
	claim := "failed"
	if err == nil {
		s.updateHarness(run, plan, profile)
		claim = s.acp.claim(ctx, entry, sp, branch != spareBranch)
		s.mu.Lock()
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunRunning, plan.ImageNotice, actor.ID)
		s.mu.Unlock()
	}
	if err != nil {
		// The run is the member's now: it ends as a session whose sandbox
		// stopped, which supervision records.
		if stopErr := s.cfg.Runtime.Stop(context.WithoutCancel(ctx), cid, s.cfg.StopGrace); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop its sandbox: %w", stopErr))
		}
		return nil, fmt.Errorf("session %s failed: claim the spare session: %w", run.ID, err)
	}
	run.Status = domain.RunRunning
	s.leaveIfRevoked(ctx, entry, run)
	slog.Info("scheduler: launch claimed the spare session", "run", run.ID, "claim", claim, "took", time.Since(began).Round(time.Millisecond))
	return s.freshen(ctx, run), nil
}

// spareFor compares sp with the session a cold launch would make of launch
// now. It returns the spare's row carrying the launch's own fields, its
// entry and the launch's environment plan, or what differs.
func (s *Scheduler) spareFor(ctx context.Context, sp *spare, launch *domain.Run, ws *domain.Workspace, actor *domain.Member, profile harness.Profile) (*domain.Run, *supervised, *EnvironmentPlan, string) {
	run, err := s.cfg.Store.GetRun(ctx, sp.run)
	if err != nil {
		return nil, nil, nil, "read its session: " + err.Error()
	}
	s.mu.Lock()
	entry := s.runs[sp.run]
	live := entry != nil && entry.spare == sp && entry.status == domain.RunRunning && !entry.paused && !entry.killRequested && !entry.finalizing
	s.mu.Unlock()
	switch {
	case !run.Spare || !live:
		return nil, nil, nil, "its session ended"
	case run.MemberID != launch.MemberID || run.AccountMember() != launch.AccountMember():
		return nil, nil, nil, "another member or account"
	case run.WorkspaceID != launch.WorkspaceID:
		return nil, nil, nil, "another workspace"
	case run.Harness != launch.Harness || run.Mode != launch.Mode:
		return nil, nil, nil, "another agent or mode"
	case run.Permission != launch.Permission || run.Limits != launch.Limits:
		return nil, nil, nil, "another permission tier or limits"
	case run.BaseCommit != launch.BaseCommit || run.BaseBranch != launch.BaseBranch:
		return nil, nil, nil, "the base moved"
	}
	if s.cfg.Profiles != nil {
		latest, err := s.cfg.Profiles.Latest(ctx, string(run.MemberID), run.Harness)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, "read the member's profile: " + err.Error()
		}
		if err == nil && latest.ID != run.ProfileSnapshotID || err != nil && run.ProfileSnapshotID != "" {
			return nil, nil, nil, "the member's profile changed"
		}
	}
	run.Task, run.Model, run.Effort, run.Via = launch.Task, launch.Model, launch.Effort, launch.Via
	run.LinearIssueURL, run.RequestedAccountMemberID, run.Fallback = launch.LinearIssueURL, launch.RequestedAccountMemberID, launch.Fallback
	run.BaseSource, run.BaseCheckedAt = launch.BaseSource, launch.BaseCheckedAt
	if err := s.checkRunReach(ctx, run); err != nil {
		return nil, nil, nil, "check workspace access: " + err.Error()
	}
	unlockCaches := sync.OnceFunc(s.lockRunCaches(run.HomeMember(), run.AccountMember()))
	defer unlockCaches()
	plan, err := s.BuildEnvironmentPlan(ctx, run, ws, actor, profile, EnvironmentPurposeRun)
	if err != nil {
		return nil, nil, nil, "plan its environment: " + err.Error()
	}
	spec, err := s.specDigest(ctx, run, actor, plan)
	if err != nil {
		return nil, nil, nil, err.Error()
	}
	if spec != sp.spec {
		return nil, nil, nil, "the session's environment changed"
	}
	return run, entry, plan, ""
}

// claim makes the open session of a claimed spare the one a launch of its
// run opens. The session stays, as an exact claim, when nothing its agent
// read at its start has changed: the run's instructions and the agent's
// own settings are the same, it is no older than spareSessionAge, and the
// agent takes what the launch chose without starting again. Otherwise, or
// with fresh, a warm claim starts the agent again, as a launch does.
func (d *acpDriver) claim(ctx context.Context, entry *supervised, sp *spare, fresh bool) string {
	unlock := d.lockOp(entry.runID)
	exact := !fresh && d.keepSession(ctx, entry, sp)
	unlock()
	if exact {
		return "exact"
	}
	d.restart(ctx, entry)
	return "warm"
}

// keepSession reports whether the spare's open session serves the launch
// that claimed it, and gives it the launch's model and effort when it does.
func (d *acpDriver) keepSession(ctx context.Context, entry *supervised, sp *spare) bool {
	d.s.mu.Lock()
	instructions, inputs, opened := sp.instructions, sp.inputs, sp.opened
	cid, coordDir := entry.containerID, entry.coordDir
	d.s.mu.Unlock()
	sess, liveErr, _ := d.liveNow(entry.runID)
	run, err := d.s.cfg.Store.GetRun(ctx, entry.runID)
	if liveErr != nil || err != nil || inputs == "" || time.Since(opened) >= spareSessionAge {
		return false
	}
	profile, _, err := d.s.launchProfile(ctx, run.MemberID, run.AccountMember(), run.Harness)
	if err != nil || !profile.ACPSystemPrompt || profile.ACPEffortArgs && run.Effort != "" || run.Task != "" {
		return false
	}
	now, err := d.s.briefedInstructions(ctx, d.s.runGuide(ctx, entry.runID, false, true, d.s.containerCLI(ctx, cid)), run.Extras, run.WorkspaceID, run.MemberID)
	if err != nil || now != instructions || d.s.sessionInputs(run, profile) != inputs {
		return false
	}
	_, _ = d.setModelAndEffort(ctx, sess, run, profile, false)
	if coordDir != "" {
		d.noteInstructions(ctx, run, true)
	}
	return true
}

// restart ends the run's agent and starts it again with a new session, as
// its first open did, with no trace of the session it replaces.
func (d *acpDriver) restart(ctx context.Context, entry *supervised) {
	unlock := d.lockOp(entry.runID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), acpConnectTimeout)
	defer cancel()
	if err := d.stopAdapterLocked(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: stop the spare session's agent", "run", entry.runID, "error", err)
	}
	if err := os.Remove(d.s.cfg.PTY.ItemLogPath(entry.runID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("scheduler: remove the spare session's items", "run", entry.runID, "error", err)
	}
	d.connectLocked(ctx, entry, openNew)
}

// noteInstructions records on the timeline how a new session was given its
// run instructions.
func (d *acpDriver) noteInstructions(ctx context.Context, run *domain.Run, systemPrompt bool) {
	c := d.s.coordinationSeam()
	if c == nil || !c.enabled {
		return
	}
	channel := "first message"
	if systemPrompt {
		channel = "system prompt"
	}
	d.s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote, "session instructions: "+channel)
}
