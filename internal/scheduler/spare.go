package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const (
	// spareWindow is how long after their last launch a member keeps a
	// spare, which also bounds how old its agent session can be.
	spareWindow = 15 * time.Minute
	// spareMemoryBytes is the room above the memory guard's calm line a
	// spare is made in: what a session VM with a started agent holds.
	spareMemoryBytes = 2 * startupMemoryBytes
)

// runHider is the bus that keeps a spare session's events from everyone.
type runHider interface {
	Hide(domain.RunID)
	Show(domain.RunID)
}

// spareTarget is the launch a member's spare is made for, their last one a
// spare can stand in for.
type spareTarget struct {
	workspace  domain.WorkspaceID
	harness    string
	permission domain.Permission
	limits     domain.RunLimits
}

// spare is a session made ahead of a launch: a hidden Enhanced run with no
// task that a launch of its member claims. run and ready are guarded by
// Scheduler.mu; spec is written once before the run's sandbox starts.
type spare struct {
	member domain.MemberID
	target spareTarget
	cancel context.CancelFunc
	done   chan struct{}
	run    domain.RunID
	// ready is set once the spare's agent session is open.
	ready bool
	// spec digests the sandbox the spare started in.
	spec string
}

// memberSpare is what the spares follow of one member.
type memberSpare struct {
	launching int
	last      time.Time
	target    *spareTarget
	spare     *spare
}

// spareable reports whether a spare can stand in for the launch of run: an
// Enhanced session on the member's own default login, in one repository at
// the workspace's base, that no connector, parent session or issue asked
// for, of an agent that reads its instructions as a system prompt and takes
// an effort on a live session.
func spareable(run *domain.Run, opts domain.LaunchOptions, profile harness.Profile) bool {
	return run.Mode == domain.LaunchACP && run.ACP && profile.ACPSystemPrompt && !profile.ACPEffortArgs &&
		run.AccountMember() == run.MemberID && run.RequestedAccountMemberID == "" && run.AccountLogin == "" && !run.AccountLoginPerUse &&
		len(run.Extras) == 0 && run.LinearIssueURL == "" && run.Via == "" && opts.FromCommit == "" && opts.ReservedRunID == ""
}

// launchBegan cancels member's spare while it still starts, since it holds
// the member's cache lock, and keeps another from starting while the launch
// runs. The returned func ends the launch: run is what it made, and target
// the spare to keep for the member after it, nil for a launch no spare can
// stand in for or one a schedule fired.
func (s *Scheduler) launchBegan(member domain.MemberID) func(run *domain.Run, target *spareTarget) {
	s.mu.Lock()
	if s.members == nil {
		s.members = make(map[domain.MemberID]*memberSpare)
	}
	m := s.members[member]
	if m == nil {
		m = &memberSpare{}
		s.members[member] = m
	}
	m.launching++
	var starting *spare
	if m.spare != nil && !m.spare.ready {
		starting, m.spare = m.spare, nil
		s.sparesKept.Add(-1)
	}
	s.mu.Unlock()
	if starting != nil {
		s.goRemoveSpare(starting)
	}
	return func(run *domain.Run, target *spareTarget) {
		s.mu.Lock()
		m.launching--
		if run != nil && target != nil {
			m.last, m.target = time.Now(), target
		}
		s.mu.Unlock()
		if run != nil {
			// After the launch's agent has opened, so the spare's start
			// takes nothing from it.
			s.acp.afterOpening(run.ID, func() { s.goSpare(member) })
		}
	}
}

// goSpare starts making member's spare when they launched within
// spareWindow, have a target and no spare, and no launch of theirs runs.
func (s *Scheduler) goSpare(member domain.MemberID) {
	_, hides := s.cfg.Bus.(runHider)
	_, caches := s.cfg.Runtime.(runtime.CacheRuntime)
	if !hides || !caches || s.recovering.Load() {
		return
	}
	s.mu.Lock()
	m := s.members[member]
	if s.superCtx.Err() != nil || m == nil || m.spare != nil || m.target == nil || m.launching > 0 || time.Since(m.last) >= spareWindow {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.superCtx)
	sp := &spare{member: member, target: *m.target, cancel: cancel, done: make(chan struct{})}
	m.spare = sp
	s.sparesKept.Add(1)
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(sp.done)
		defer cancel()
		s.makeSpare(ctx, sp)
	}()
}

func (s *Scheduler) makeSpare(ctx context.Context, sp *spare) {
	_, err := s.launch(ctx, sp.target.workspace, sp.member, sp.member, "", sp.target.harness, domain.LaunchACP,
		domain.LaunchOptions{Permission: sp.target.permission, Limits: sp.target.limits, RequestedAccount: sp.member}, sp)
	s.mu.Lock()
	run := sp.run
	s.mu.Unlock()
	if err == nil {
		if err = s.acp.awaitOpening(ctx, run); err == nil {
			_, err, _ = s.acp.liveNow(run)
		}
	}
	s.mu.Lock()
	m := s.members[sp.member]
	kept := err == nil && m != nil && m.spare == sp
	if kept {
		sp.ready = true
	} else if m != nil && m.spare == sp {
		m.spare = nil
		s.sparesKept.Add(-1)
	}
	s.mu.Unlock()
	if kept {
		slog.Info("scheduler: spare session ready", "member", sp.member, "run", run)
		return
	}
	if err != nil && ctx.Err() == nil {
		slog.Info("scheduler: no spare session", "member", sp.member, "error", err)
	}
	s.removeSpareRun(run)
}

// hideSpare records the run a spare was created as, before anything is
// published about it.
func (s *Scheduler) hideSpare(sp *spare, run domain.RunID) {
	s.cfg.Bus.(runHider).Hide(run)
	s.mu.Lock()
	sp.run = run
	s.mu.Unlock()
}

// spareReady reports whether member has a spare a launch could claim.
func (s *Scheduler) spareReady(member domain.MemberID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[member]
	return m != nil && m.spare != nil && m.spare.ready
}

// takeSpare gives the caller member's ready spare, which it must claim or
// remove.
func (s *Scheduler) takeSpare(member domain.MemberID) *spare {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[member]
	if m == nil || m.spare == nil || !m.spare.ready {
		return nil
	}
	sp := m.spare
	m.spare = nil
	s.sparesKept.Add(-1)
	return sp
}

// goRemoveSpare destroys a spare its member no longer holds, and closes the
// returned channel once it is gone.
func (s *Scheduler) goRemoveSpare(sp *spare) <-chan struct{} {
	removed := make(chan struct{})
	sp.cancel()
	s.mu.Lock()
	if s.superCtx.Err() != nil {
		// A server that stops leaves the spare for the next start to remove.
		s.mu.Unlock()
		close(removed)
		return removed
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(removed)
		<-sp.done
		s.mu.Lock()
		run, ready := sp.run, sp.ready
		s.mu.Unlock()
		// makeSpare removes the run of a spare it did not keep.
		if ready {
			s.removeSpareRun(run)
		}
	}()
	return removed
}

// removeSpareRun deletes a spare's run with its sandbox, checkout and the
// branch name it reserved. It outlives the caller: a spare half removed
// would stay hidden and hold memory. The run is supervised or ended, which
// recovery sees to for the spares a stopped server left: deleting one
// nothing supervises would leave its sandbox.
func (s *Scheduler) removeSpareRun(run domain.RunID) {
	if run == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	row, err := s.cfg.Store.GetRun(ctx, run)
	switch {
	case errors.Is(err, store.ErrNotFound):
		err = nil
	case err == nil && !row.Spare:
		return
	case err == nil:
		if err = s.DeleteRun(ctx, run, ""); err == nil && row.Branch != "" {
			err = s.cfg.Git.ReleaseRunBranch(ctx, row.WorkspaceID, row.Branch, row.BaseCommit)
		}
	}
	if err != nil {
		slog.Warn("scheduler: remove a spare session; trying again", "run", run, "error", err)
		s.mu.Lock()
		s.sparesLeft = append(s.sparesLeft, run)
		s.mu.Unlock()
		return
	}
	s.cfg.Bus.(runHider).Show(run)
}

// dropSpares removes the spares match selects, every one with a nil match,
// and reports whether there was one. They go before anything is taken from
// a member's session. With wait it returns once their sandboxes are gone.
func (s *Scheduler) dropSpares(ctx context.Context, wait bool, match func(*spare) bool) bool {
	var removed []<-chan struct{}
	s.mu.Lock()
	var dropped []*spare
	for _, m := range s.members {
		if m.spare != nil && (match == nil || match(m.spare)) {
			dropped = append(dropped, m.spare)
			m.spare = nil
			s.sparesKept.Add(-1)
		}
	}
	s.mu.Unlock()
	for _, sp := range dropped {
		slog.Info("scheduler: removed a spare session", "member", sp.member)
		removed = append(removed, s.goRemoveSpare(sp))
	}
	for _, done := range removed {
		if !wait {
			break
		}
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	return len(dropped) > 0
}

// DropSpares removes the spare kept for member and forgets what it was made
// for, so none is made again before their next launch. With wait it returns
// once the spare's sandbox is gone.
func (s *Scheduler) DropSpares(ctx context.Context, member domain.MemberID, wait bool) {
	s.mu.Lock()
	if m := s.members[member]; m != nil {
		m.target = nil
	}
	s.mu.Unlock()
	s.dropSpares(ctx, wait, func(sp *spare) bool { return sp.member == member })
}

// removeWorkspaceSpares removes the spares made in workspace before it is
// deleted, and waits for them.
func (s *Scheduler) removeWorkspaceSpares(ctx context.Context, workspace domain.WorkspaceID) {
	s.mu.Lock()
	for _, m := range s.members {
		if m.target != nil && m.target.workspace == workspace {
			m.target = nil
		}
	}
	s.mu.Unlock()
	s.dropSpares(ctx, true, func(sp *spare) bool { return sp.target.workspace == workspace })
}

// sweepSpares removes the spare of a member who has not launched within
// spareWindow and one whose run ended on its own, and tries again the
// removals that failed.
func (s *Scheduler) sweepSpares() {
	var gone []*spare
	s.mu.Lock()
	left := s.sparesLeft
	s.sparesLeft = nil
	if s.superCtx.Err() != nil {
		left = nil
	}
	s.wg.Add(len(left))
	for member, m := range s.members {
		old := time.Since(m.last) >= spareWindow
		switch sp := m.spare; {
		case sp == nil:
			if old && m.launching == 0 {
				delete(s.members, member)
			}
		case sp.ready && (old || s.runs[sp.run] == nil || s.runs[sp.run].status.Terminal()):
			gone = append(gone, sp)
			m.spare = nil
			s.sparesKept.Add(-1)
		}
	}
	s.mu.Unlock()
	for _, sp := range gone {
		slog.Info("scheduler: removed a spare session nobody claimed", "member", sp.member, "run", sp.run)
		s.goRemoveSpare(sp)
	}
	for _, run := range left {
		go func() {
			defer s.wg.Done()
			s.removeSpareRun(run)
		}()
	}
}

// removeLeftoverSpares removes the spares a stopped server left, once
// recovery supervises again those that still ran: a spare is never kept
// across a restart, as the member's next launch may no longer match it.
func (s *Scheduler) removeLeftoverSpares(ctx context.Context) {
	hider, hides := s.cfg.Bus.(runHider)
	spares, err := s.cfg.Store.ListSpareRuns(ctx)
	if err != nil || !hides {
		if err != nil {
			slog.Warn("scheduler: recovery: list the spare sessions the last server left", "error", err)
		}
		return
	}
	s.mu.Lock()
	for _, run := range spares {
		hider.Hide(run.ID)
		if entry := s.runs[run.ID]; entry != nil {
			entry.spare = &spare{member: run.MemberID, run: run.ID}
		}
	}
	s.wg.Add(len(spares))
	s.mu.Unlock()
	for _, run := range spares {
		go func() {
			defer s.wg.Done()
			s.removeSpareRun(run.ID)
		}()
	}
}

// specDigest digests the sandbox a launch of run starts in: its image,
// user, limits, mounts, environment and secrets, the agent programs
// installed in the member's home, and the command that starts the agent.
// plan is as BuildEnvironmentPlan returned it.
func (s *Scheduler) specDigest(ctx context.Context, run *domain.Run, actor *domain.Member, plan *EnvironmentPlan) (string, error) {
	limits, err := s.runLimits(ctx, run.Limits)
	if err != nil {
		return "", err
	}
	spec, err := json.Marshal(s.containerSpec(run, actor, nil, plan, limits))
	if err != nil {
		return "", fmt.Errorf("scheduler: digest the session's sandbox: %w", err)
	}
	_, argv, err := s.launchProfile(ctx, run.MemberID, run.AccountMember(), run.Harness)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(spec)
	_, _ = fmt.Fprintf(h, "\nagent %q", argv[domain.LaunchACP])
	for _, program := range plan.Programs {
		// A program disk holds the version installed when the VM started.
		path := filepath.Join(program.Home, ".local", "bin", program.Executable)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("scheduler: resolve the installed %s: %w", program.Executable, err)
		}
		var size int64
		var modified time.Time
		if info, statErr := os.Stat(path); statErr == nil {
			size, modified = info.Size(), info.ModTime()
		}
		_, _ = fmt.Fprintf(h, "\nprogram %q %q %d %d", program.Executable, resolved, size, modified.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
