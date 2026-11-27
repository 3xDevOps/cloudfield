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
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const (
	// spareActiveFor is how long a member with no open event stream stays
	// active after their last launch or stream.
	spareActiveFor = 10 * time.Minute
	// spareSessionAge is how old a spare's agent session may grow: it holds
	// the date and the output of the member's own hooks from its start.
	spareSessionAge = 15 * time.Minute
	// spareRetry is how long a member goes without a spare after one failed
	// to start or was given up for memory.
	spareRetry = 5 * time.Minute
	// spareMemoryBytes is the room above the floor a spare is made in: the
	// 0.8 GiB a session VM with an idle agent holds, and the start of the
	// launch that does not claim it.
	spareMemoryBytes = 3 * startupMemoryBytes
)

// runHider is the bus that keeps a spare session's events from everyone.
type runHider interface {
	Hide(domain.RunID)
	Show(domain.RunID)
}

// spareTarget is the launch a member's spare is made for, their last one.
type spareTarget struct {
	workspace  domain.WorkspaceID
	harness    string
	permission domain.Permission
	limits     domain.RunLimits
}

// spare is a session made ahead of a launch: a hidden Enhanced run with no
// task that a launch of its member takes over. run, ready, reopening,
// instructions, inputs and opened are guarded by Scheduler.mu; spec is
// written once before the run's sandbox starts.
type spare struct {
	member domain.MemberID
	target spareTarget
	cancel context.CancelFunc
	done   chan struct{}
	run    domain.RunID
	// ready is set once the spare's session is open, and reopening while
	// the sweep opens it again.
	ready     bool
	reopening bool
	// spec digests the sandbox the spare started in.
	spec string
	// instructions and inputs are what its agent session read when it
	// opened at opened; inputs is empty for an agent whose reads are unknown.
	instructions string
	inputs       string
	opened       time.Time
}

// memberSpare is what the spares follow of one member.
type memberSpare struct {
	streams   int
	launching int
	seen      time.Time
	retryAt   time.Time
	target    *spareTarget
	spare     *spare
}

func (s *Scheduler) memberSpareLocked(member domain.MemberID) *memberSpare {
	if s.members == nil {
		s.members = make(map[domain.MemberID]*memberSpare)
	}
	m := s.members[member]
	if m == nil {
		m = &memberSpare{}
		s.members[member] = m
	}
	return m
}

func (m *memberSpare) active(now time.Time) bool {
	return m.streams > 0 || now.Sub(m.seen) < spareActiveFor
}

// MemberStream records an event stream member opened, until the returned
// func runs: a member watching is one about to launch.
func (s *Scheduler) MemberStream(member domain.MemberID) func() {
	s.mu.Lock()
	m := s.memberSpareLocked(member)
	m.streams++
	s.mu.Unlock()
	s.goSpare(member)
	return sync.OnceFunc(func() {
		s.mu.Lock()
		m.streams--
		m.seen = time.Now()
		s.mu.Unlock()
	})
}

// spareable reports whether a launch is one a spare can stand in for: an
// Enhanced session on the member's own account, in one repository, at the
// workspace's base, that no connector or parent session asked for.
func spareable(run *domain.Run, opts domain.LaunchOptions) bool {
	return run.Mode == domain.LaunchACP && run.ACP && opts.ReservedRunID == "" && len(run.Extras) == 0 &&
		opts.FromCommit == "" && run.AccountMember() == run.MemberID && run.RequestedAccountMemberID == "" && run.Via == ""
}

// launchBegan stops member's spare from starting while a launch of theirs
// runs, since both would wait on the member's caches. The returned func
// ends the launch, whose run becomes the next spare's target when a spare
// can stand in for a launch like it.
func (s *Scheduler) launchBegan(member domain.MemberID) func(run *domain.Run, target bool) {
	s.mu.Lock()
	m := s.memberSpareLocked(member)
	m.launching++
	var starting *spare
	if m.spare != nil && !m.spare.ready {
		starting, m.spare = m.spare, nil
	}
	s.mu.Unlock()
	if starting != nil {
		s.goRemoveSpare(starting)
	}
	return func(run *domain.Run, target bool) {
		s.mu.Lock()
		m.launching--
		m.seen = time.Now()
		if target {
			m.target = &spareTarget{workspace: run.WorkspaceID, harness: run.Harness, permission: run.Permission, limits: run.Limits}
		}
		s.mu.Unlock()
		s.goSpare(member)
	}
}

// goSpare starts making member's spare when they are active, have launched
// a session it can stand in for, and have none.
func (s *Scheduler) goSpare(member domain.MemberID) {
	if _, hides := s.cfg.Bus.(runHider); !hides || s.recovering.Load() || s.superCtx.Err() != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	m := s.members[member]
	if m == nil || m.spare != nil || m.target == nil || m.launching > 0 || !m.active(now) || now.Before(m.retryAt) {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.superCtx)
	sp := &spare{member: member, target: *m.target, cancel: cancel, done: make(chan struct{})}
	m.spare = sp
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
		_, err, _ = s.acp.liveNow(run)
	}
	s.mu.Lock()
	m := s.members[sp.member]
	kept := err == nil && m != nil && m.spare == sp
	if kept {
		sp.ready = true
	} else if m != nil && m.spare == sp {
		m.spare = nil
		m.retryAt = time.Now().Add(spareRetry)
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

// takeSpare gives the caller member's ready spare, which it must claim or
// remove.
func (s *Scheduler) takeSpare(member domain.MemberID) *spare {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[member]
	if m == nil || m.spare == nil || !m.spare.ready || m.spare.reopening {
		return nil
	}
	sp := m.spare
	m.spare = nil
	return sp
}

// goRemoveSpare destroys a spare its member no longer holds.
func (s *Scheduler) goRemoveSpare(sp *spare) <-chan struct{} {
	removed := make(chan struct{})
	sp.cancel()
	s.wg.Add(1)
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
// would stay hidden and hold memory.
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
		slog.Warn("scheduler: remove a spare session", "run", run, "error", err)
		return
	}
	if hider, hides := s.cfg.Bus.(runHider); hides {
		hider.Show(run)
	}
}

// dropSpares gives up every spare for memory, and reports whether there was
// one. With wait it returns once their sandboxes are gone.
func (s *Scheduler) dropSpares(ctx context.Context, wait bool) bool {
	retry := time.Now().Add(spareRetry)
	var dropped []*spare
	s.mu.Lock()
	for _, m := range s.members {
		if m.spare != nil {
			dropped = append(dropped, m.spare)
			m.spare, m.retryAt = nil, retry
		}
	}
	s.mu.Unlock()
	for _, sp := range dropped {
		slog.Info("scheduler: gave up a spare session for memory", "member", sp.member)
		removed := s.goRemoveSpare(sp)
		if wait {
			select {
			case <-removed:
			case <-ctx.Done():
			}
		}
	}
	return len(dropped) > 0
}

// sweepSpares removes the spare of a member who went inactive, of one whose
// run ended, and every spare when the host has no room left for a launch
// beside them; opens again an agent session grown old; and makes the spare
// an active member lacks.
func (s *Scheduler) sweepSpares(ctx context.Context) {
	if available, floor, err := s.hostMemory(); err == nil && available < floor+startupMemoryBytes {
		s.dropSpares(ctx, false)
		return
	}
	now := time.Now()
	var gone, old []*spare
	var lacking []domain.MemberID
	s.mu.Lock()
	for member, m := range s.members {
		sp := m.spare
		switch {
		case sp == nil:
			if m.active(now) {
				lacking = append(lacking, member)
			} else if m.streams == 0 && m.launching == 0 {
				delete(s.members, member)
			}
		case !sp.ready || sp.reopening:
		case !m.active(now) || s.runs[sp.run] == nil || s.runs[sp.run].status != domain.RunRunning:
			gone = append(gone, sp)
			m.spare, m.retryAt = nil, now.Add(spareRetry)
		case now.Sub(sp.opened) >= spareSessionAge:
			old = append(old, sp)
			sp.reopening = true
		}
	}
	s.mu.Unlock()
	for _, sp := range gone {
		s.goRemoveSpare(sp)
	}
	for _, sp := range old {
		s.reopenSpare(ctx, sp)
	}
	for _, member := range lacking {
		s.goSpare(member)
	}
}

// reopenSpare starts a spare's agent session again, unless a launch of the
// member's has taken the spare away meanwhile.
func (s *Scheduler) reopenSpare(ctx context.Context, sp *spare) {
	s.mu.Lock()
	entry := s.runs[sp.run]
	s.mu.Unlock()
	err := errors.New("the session is busy")
	if entry != nil && entry.lifecycleMu.TryLock() {
		s.acp.restart(ctx, entry)
		entry.lifecycleMu.Unlock()
		_, err, _ = s.acp.liveNow(sp.run)
	}
	s.mu.Lock()
	sp.reopening = false
	m := s.members[sp.member]
	failed := err != nil && m != nil && m.spare == sp
	if failed {
		m.spare, m.retryAt = nil, time.Now().Add(spareRetry)
	}
	s.mu.Unlock()
	if failed {
		slog.Info("scheduler: gave up a spare session whose agent did not start again", "member", sp.member, "error", err)
		s.goRemoveSpare(sp)
	}
}

// removeLeftoverSpares deletes the spares a stopped server left: they are
// never recovered, as nobody knows of them.
func (s *Scheduler) removeLeftoverSpares(ctx context.Context) error {
	hider, hides := s.cfg.Bus.(runHider)
	spares, err := s.cfg.Store.ListSpareRuns(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: recovery: list spare sessions: %w", err)
	}
	for _, run := range spares {
		if hides {
			hider.Hide(run.ID)
		}
		s.removeSpareRun(run.ID)
	}
	return nil
}

// ForgetSpare removes the spare kept for member, whose sessions move to
// another member, and waits for it.
func (s *Scheduler) ForgetSpare(member domain.MemberID) {
	s.mu.Lock()
	var sp *spare
	if m := s.members[member]; m != nil {
		sp, m.spare, m.target = m.spare, nil, nil
	}
	s.mu.Unlock()
	if sp != nil {
		<-s.goRemoveSpare(sp)
	}
}

// removeWorkspaceSpares removes the spares made in workspace before it is
// deleted, and waits for them.
func (s *Scheduler) removeWorkspaceSpares(workspace domain.WorkspaceID) {
	var dropped []*spare
	s.mu.Lock()
	for _, m := range s.members {
		if m.target != nil && m.target.workspace == workspace {
			m.target = nil
		}
		if m.spare != nil && m.spare.target.workspace == workspace {
			dropped = append(dropped, m.spare)
			m.spare = nil
		}
	}
	s.mu.Unlock()
	for _, sp := range dropped {
		<-s.goRemoveSpare(sp)
	}
}

// specDigest digests the sandbox a launch of run starts in: its image,
// user, limits, mounts, environment and secrets, and the agent programs
// installed in the member's home. plan is as BuildEnvironmentPlan returned
// it.
func (s *Scheduler) specDigest(ctx context.Context, run *domain.Run, actor *domain.Member, plan *EnvironmentPlan) (string, error) {
	limits, err := s.runLimits(ctx, run.Limits)
	if err != nil {
		return "", err
	}
	spec, err := json.Marshal(s.containerSpec(run, actor, nil, plan, limits))
	if err != nil {
		return "", fmt.Errorf("scheduler: digest the session's sandbox: %w", err)
	}
	h := sha256.New()
	h.Write(spec)
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
		fmt.Fprintf(h, "\nprogram %q %q %d %d", program.Executable, resolved, size, modified.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// spareInputs digests what the agent of a spare reads from its member's
// home as its session opens; empty for any other run.
func (s *Scheduler) spareInputs(entry *supervised, run *domain.Run, profile harness.Profile) string {
	s.mu.Lock()
	sp := entry.spare
	s.mu.Unlock()
	if sp == nil {
		return ""
	}
	return s.sessionInputs(run, profile)
}

func (s *Scheduler) sessionInputs(run *domain.Run, profile harness.Profile) string {
	if profile.SessionInputs == nil || s.cfg.Homes == nil {
		return ""
	}
	home, err := s.cfg.Homes.Path(run.HomeMember())
	if err == nil {
		var digest string
		if digest, err = profile.SessionInputs(home, s.cfg.WorktreeMount); err == nil {
			return digest
		}
	}
	slog.Warn("scheduler: digest what the agent reads at its start", "run", run.ID, "error", err)
	return ""
}

// spareOpened records what a spare's agent session was opened with.
func (s *Scheduler) spareOpened(entry *supervised, instructions, inputs string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp := entry.spare; sp != nil {
		sp.instructions, sp.inputs, sp.opened = instructions, inputs, time.Now()
	}
}
