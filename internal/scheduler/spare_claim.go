package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// claimSpare makes launch out of its member's spare when the spare is the
// session a cold launch would start now, and returns the run. It returns
// nil, nil for a miss, a launch that goes on cold: there is no ready spare,
// or it differs and is removed. Until the spare's row is claimed nothing of
// it is visible, so every refusal before that is a miss; after it the
// launch is this run's, whatever happens.
func (s *Scheduler) claimSpare(ctx context.Context, launch *domain.Run, ws *domain.Workspace, actor *domain.Member, profile harness.Profile, opts domain.LaunchOptions) (*domain.Run, error) {
	sp := s.takeSpare(launch.MemberID)
	if sp == nil {
		return nil, nil
	}
	began := time.Now()
	miss := func(reason string) {
		slog.Info("scheduler: launch missed the spare session", "member", sp.member, "run", sp.run, "reason", reason)
		s.goRemoveSpare(sp)
	}
	run, entry, plan, differs := s.spareFor(ctx, sp, launch, ws, actor, profile)
	if differs == "" && !entry.lifecycleMu.TryLock() {
		differs = "its session is busy"
	}
	if differs != "" {
		miss(differs)
		return nil, nil
	}
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	cid, session := entry.containerID, entry.agentSessionID
	s.mu.Unlock()
	if err := s.nameSpareBranch(ctx, cid, run); err != nil {
		miss("name its branch: " + err.Error())
		return nil, nil
	}
	// The run forgets its agent session until the task has reached it, so a
	// server that restarts in between sends the task to a new one. The
	// sidecar goes first: a row claimed beside a sidecar that still names
	// the session would be taken up again without its task.
	s.mu.Lock()
	entry.agentSessionID = ""
	err := s.writeSidecar(entry.sidecar())
	s.mu.Unlock()
	// The brake leaves a spare's VM alone, and must leave the session it
	// becomes alone until its agent has the task.
	defer s.needGuest(cid)()
	if err == nil {
		err = s.cfg.Store.ClaimSpareRun(ctx, run)
	}
	if err != nil {
		miss("claim its session: " + err.Error())
		return nil, nil
	}
	s.cfg.Bus.(runHider).Show(run.ID)
	// Only now, when nothing can remove the run as a spare, may its cache
	// be published.
	if err = s.cfg.Runtime.(runtime.CacheRuntime).PublishCache(ctx, cid); err != nil {
		slog.Warn("scheduler: the claimed session's cache stays unpublished", "run", run.ID, "error", err)
	}
	if opts.Created != nil {
		opts.Created(run.ID)
	}
	if opts.Admitted != nil {
		opts.Admitted()
	}
	s.mu.Lock()
	entry.spare, entry.task, entry.status, entry.startedAt = nil, run.Task, domain.RunQueued, time.Now().UTC()
	err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunQueued, domain.RunProvisioning, "", actor.ID)
	if err == nil {
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunRunning, plan.ImageNotice, actor.ID)
	}
	s.mu.Unlock()
	if err == nil {
		// updateHarness writes the member's cache records, under their
		// cache lock. It is never waited for: an update that is due and
		// skipped here starts with the member's next launch.
		unlock, locked := func() {}, true
		if s.cfg.Homes != nil {
			unlock, locked = s.cfg.Homes.TryLockCaches(run.HomeMember())
		}
		if locked {
			s.updateHarness(run, plan, profile)
			unlock()
		}
		err = s.acp.claim(ctx, entry, run, profile, session)
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
	slog.Info("scheduler: launch claimed the spare session", "member", sp.member, "run", run.ID, "took", time.Since(began).Round(time.Millisecond))
	return s.freshen(ctx, run), nil
}

// nameSpareBranch gives the spare whose row run is the branch a cold launch
// of run's task reserves: in the workspace's repository, in the row and in
// the checkout of the VM cid. Nothing of the spare is visible yet, so a
// failure is a miss and no session ever shows the name it was made with.
func (s *Scheduler) nameSpareBranch(ctx context.Context, cid runtime.ID, run *domain.Run) error {
	made := run.Branch
	branch, err := s.cfg.Git.RenameRunBranch(ctx, run.ID, run.Task)
	if err != nil || branch == made {
		return err
	}
	run.Branch = branch
	if err = s.cfg.Store.SetRunCheckout(ctx, run.ID, run.Worktree, branch); err != nil {
		return err
	}
	code, _, stderr, err := s.cfg.Runtime.Exec(ctx, cid, []string{"git", "branch", "-m", made, branch}, s.cfg.WorktreeMount)
	if err == nil && code != 0 {
		err = fmt.Errorf("git branch -m exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return err
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
	live := entry != nil && entry.spare == sp && !entry.status.Terminal() && !entry.paused && !entry.killRequested && !entry.finalizing
	s.mu.Unlock()
	if _, liveErr, _ := s.acp.liveNow(sp.run); !run.Spare || !live || liveErr != nil {
		return nil, nil, nil, "its session ended"
	}
	switch {
	case run.MemberID != launch.MemberID || run.Account() != launch.Account():
		return nil, nil, nil, "another member or account"
	case run.WorkspaceID != launch.WorkspaceID:
		return nil, nil, nil, "another workspace"
	case run.Harness != launch.Harness || run.Mode != launch.Mode:
		return nil, nil, nil, "another agent or mode"
	case sp.target.permission != launch.Permission || sp.target.limits != launch.Limits:
		return nil, nil, nil, "another permission tier or limits"
	case run.BaseCommit != launch.BaseCommit || run.BaseBranch != launch.BaseBranch:
		return nil, nil, nil, "the base moved"
	}
	if s.cfg.Profiles != nil {
		latest, latestErr := s.cfg.Profiles.Latest(ctx, string(run.MemberID), run.Harness)
		if latestErr != nil && !errors.Is(latestErr, store.ErrNotFound) {
			return nil, nil, nil, "read the member's profile: " + latestErr.Error()
		}
		if latestErr == nil && latest.ID != run.ProfileSnapshotID || latestErr != nil && run.ProfileSnapshotID != "" {
			return nil, nil, nil, "the member's profile changed"
		}
	}
	run.Task, run.Model, run.Effort, run.Fallback = launch.Task, launch.Model, launch.Effort, launch.Fallback
	run.BaseSource, run.BaseCheckedAt = launch.BaseSource, launch.BaseCheckedAt
	if err = s.checkRunReach(ctx, run); err != nil {
		return nil, nil, nil, "check workspace access: " + err.Error()
	}
	// Never waited for: the launch holds its caller's admission, and
	// another start of the member's may hold the lock through a VM boot.
	if s.cfg.Homes != nil {
		unlock, ok := s.cfg.Homes.TryLockCaches(run.HomeMember())
		if !ok {
			return nil, nil, nil, "the member's caches are busy"
		}
		defer unlock()
	}
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

// claim gives the open session of a claimed spare what the launch chose,
// without starting its agent again: the model and effort, then the task as
// its first prompt, as the open of a new session does. session is the agent
// session the run forgot for the claim, recorded again once it has the task.
func (d *acpDriver) claim(ctx context.Context, entry *supervised, run *domain.Run, profile harness.Profile, session string) error {
	defer d.lockOp(run.ID)()
	sess, err, _ := d.liveNow(run.ID)
	if err != nil {
		return err
	}
	_, _ = d.setModelAndEffort(ctx, sess, run, profile, false)
	d.s.mu.Lock()
	coordDir := entry.coordDir
	d.s.mu.Unlock()
	if coordDir != "" {
		d.noteInstructions(ctx, run, true)
	}
	if run.Task != "" {
		if _, err := sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(run.Task)}, false, nil); err != nil {
			slog.Warn("scheduler: send the task to the agent", "run", run.ID, "error", err)
		}
	}
	if err := d.s.cfg.Store.SetRunAgentSession(ctx, run.ID, session); err != nil {
		slog.Warn("scheduler: record agent session", "run", run.ID, "error", err)
	}
	d.record(entry, func() { entry.agentSessionID = session })
	return nil
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
