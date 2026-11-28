package scheduler

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/profile"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

// spareRuntime is the session VM runtime as the spares need it: its VMs
// save, freeze and report the host's memory, and a VM's cache disk stays
// unpublished until PublishCache.
type spareRuntime struct {
	*freezeRuntime
	cacheMu    sync.Mutex
	published  []runtime.ID
	publishErr error
	// order is what the runtime was asked to do to a VM, in order.
	order []string
	// startHeld is closed when the next spare's Start begins, which then
	// waits for its launch to be cancelled; heldVM is that spare's VM,
	// whose Destroy waits for destroyGate.
	startHeld   chan struct{}
	heldVM      runtime.ID
	destroyGate chan struct{}
}

func (r *spareRuntime) PublishCache(_ context.Context, id runtime.ID) error {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.publishErr != nil {
		return r.publishErr
	}
	r.published = append(r.published, id)
	return nil
}

func (r *spareRuntime) note(what string, id runtime.ID) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.order = append(r.order, what+" "+string(id))
}

func (r *spareRuntime) did() []string {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	return slices.Clone(r.order)
}

func (r *spareRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	r.cacheMu.Lock()
	gate := r.destroyGate
	if id != r.heldVM {
		gate = nil
	}
	r.cacheMu.Unlock()
	if gate != nil {
		<-gate
	}
	r.note("destroy", id)
	// A VM that is gone gives its memory back.
	r.memMu.Lock()
	r.available += r.vm[id]
	delete(r.vm, id)
	r.memMu.Unlock()
	return r.freezeRuntime.Destroy(ctx, id)
}

func (r *spareRuntime) Save(ctx context.Context, id runtime.ID) error {
	r.note("save", id)
	return r.freezeRuntime.Save(ctx, id)
}

func (r *spareRuntime) Freeze(id runtime.ID) (bool, error) {
	r.note("freeze", id)
	return r.freezeRuntime.Freeze(id)
}

// newSpareEnv's scheduler reads spare sessions, as the server's does, while
// e.db is the handle every other service holds. Its host has 32 GiB, 24 GiB
// of it available.
func newSpareEnv(t *testing.T, opts ...func(*Config)) (*testEnv, *spareRuntime) {
	t.Helper()
	var sr *spareRuntime
	e, _ := newMemoryEnvWith(t, func(mr *memoryRuntime) runtime.Runtime {
		sr = &spareRuntime{freezeRuntime: &freezeRuntime{memoryRuntime: mr, savedFrozen: map[runtime.ID]bool{}}}
		return sr
	}, append([]func(*Config){withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")), func(cfg *Config) {
		cfg.Store = storetest.Unscoped{DB: cfg.Store.(storetest.Unscoped).WithSpares()}
	}}, opts...)...)
	withCoordination(t, e)
	return e, sr
}

// launchClaude launches an Enhanced Claude Code session, the launch a spare
// can stand in for.
func (e *testEnv) launchClaude(t *testing.T, task string, opts domain.LaunchOptions) *domain.Run {
	t.Helper()
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "claude", domain.LaunchACP, opts)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run
}

// spare is the run of the member's ready spare, or "".
func (e *testEnv) spare() domain.RunID {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if m := e.sched.members[e.member.ID]; m != nil && m.spare != nil && m.spare.ready {
		return m.spare.run
	}
	return ""
}

func (e *testEnv) waitSpare(t *testing.T) domain.RunID {
	t.Helper()
	waitFor(t, "the spare session", func() bool { return e.spare() != "" })
	return e.spare()
}

// waitNoSpares waits until no spare is kept, made or being removed, and
// nothing of one is left.
func (e *testEnv) waitNoSpares(t *testing.T) {
	t.Helper()
	waitFor(t, "the spare sessions to go", func() bool {
		e.sched.mu.Lock()
		for _, m := range e.sched.members {
			if m.spare != nil {
				e.sched.mu.Unlock()
				return false
			}
		}
		e.sched.mu.Unlock()
		spares, err := e.sched.cfg.Store.ListSpareRuns(t.Context())
		return err == nil && len(spares) == 0
	})
}

// launchTrail collects what sub receives about run, but for its agent's cost
// and title, until its first turn has ended and the run is parked for its
// member.
func launchTrail(t *testing.T, sub events.Subscription, run domain.RunID) []string {
	t.Helper()
	var got []string
	defer func() {
		if t.Failed() {
			t.Logf("events of %s so far: %q", run, got)
		}
	}()
	waitFor(t, "the events of a launch", func() bool {
		for {
			select {
			case ev := <-sub.Events():
				if ev.RunID != run {
					continue
				}
				switch p := ev.Payload.(type) {
				case events.RunStatusPayload:
					got = append(got, string(p.From)+" -> "+string(p.To)+" "+p.Reason)
				case events.TimelinePayload:
					got = append(got, "note: "+p.Message)
				case events.RunCostPayload, events.RunTitlePayload:
					// The agent's, whenever it sends them.
				default:
					got = append(got, string(ev.Type))
				}
			default:
				return slices.Contains(got, "running -> needs-attention "+turnEndedReason)
			}
		}
	})
	return got
}

func TestALaunchClaimsTheSpareWithoutStartingItsAgentAgain(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	all, spares := e.subscribe(t), e.subscribe(t)
	first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
	cold := launchTrail(t, all, first.ID)

	spare := e.waitSpare(t)
	if spare == first.ID {
		t.Fatal("the spare is the launched session")
	}
	// Before the claim nothing of the spare shows anywhere a member looks.
	for len(spares.Events()) > 0 {
		if ev := <-spares.Events(); ev.RunID == spare {
			t.Fatalf("an event of an unclaimed spare: %+v", ev)
		}
	}
	if _, err := e.db.GetRun(t.Context(), spare); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun of a spare on a member's handle = %v, want not found", err)
	}
	active, err := e.db.ListActiveRuns(t.Context())
	if err != nil || len(active) != 1 || active[0].ID != first.ID {
		t.Fatalf("active sessions = %v, %v; want only the launched one", runIDsOf(active), err)
	}
	if busy := e.sched.Busy(t.Context()); busy.Runs != 1 {
		t.Fatalf("server busy = %+v, want the one launched session", busy)
	}
	e.sched.mu.Lock()
	held := heldAwakeLocked(e.sched.runs[spare])
	e.sched.mu.Unlock()
	if held == "" {
		t.Fatal("a spare session may be put to sleep")
	}
	if len(rt.published) != 0 {
		t.Fatalf("published the cache of %v before any claim", rt.published)
	}
	if spec := e.container(t, spare).spec; !spec.CacheUnpublished {
		t.Fatal("the spare's sandbox may publish its cache")
	}

	var created []domain.RunID
	agents := len(rt.all())
	claimed := e.launchClaude(t, "say pong again", domain.LaunchOptions{Model: "opus", Effort: "high", Created: func(id domain.RunID) { created = append(created, id) }})
	if claimed.ID != spare {
		t.Fatalf("the second launch made %s, want the spare %s", claimed.ID, spare)
	}
	if claimed.Task != "say pong again" || claimed.Status != domain.RunRunning || claimed.Spare || !slices.Equal(created, []domain.RunID{spare}) {
		t.Fatalf("claimed session = %+v, created %v", claimed, created)
	}
	if got := len(rt.all()); got != agents {
		t.Fatalf("the claim started %d more agents", got-agents)
	}
	waitItems(t, e.sched, spare, "the claimed task's turn", turnEnded("end_turn", 1))
	wire := &rt.all()[agents-1].wire
	if prompts := wire.prompts(t); len(prompts) != 1 || !slices.Equal(textOf(prompts[0]), []string{"say pong again"}) {
		t.Fatalf("the spare's agent was prompted %v, want the launch's task alone", prompts)
	}
	row, err := e.db.GetRun(t.Context(), spare)
	if err != nil || row.Model != "opus" || row.Effort != "high" || row.HarnessSessionID == "" {
		t.Fatalf("claimed row = %+v, %v; want the launch's model and effort and its agent session", row, err)
	}
	if sess := e.sched.acp.session(spare); sess == nil || sess.Model() != "opus" {
		t.Fatalf("the claimed session's model = %v, want opus", sess)
	}
	if !slices.Equal(rt.published, []runtime.ID{e.cid(t, spare)}) {
		t.Fatalf("published caches = %v, want the claimed session's", rt.published)
	}
	if got := launchTrail(t, all, spare); !slices.Equal(got, cold) {
		t.Fatalf("a claim published\n%q\na cold launch\n%q", got, cold)
	}
	// The claim leaves the member a new spare.
	if next := e.waitSpare(t); next == spare {
		t.Fatal("the claimed session is still the member's spare")
	}
}

func runIDsOf(runs []*domain.Run) []domain.RunID {
	var ids []domain.RunID
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// holdSpareStart makes the next spare's Runtime.Start wait for its launch
// to be cancelled, and its Destroy for the returned release.
func (r *spareRuntime) holdSpareStart() (started <-chan struct{}, release func()) {
	s, gate := make(chan struct{}), make(chan struct{})
	r.cacheMu.Lock()
	r.startHeld, r.destroyGate = s, gate
	r.cacheMu.Unlock()
	return s, sync.OnceFunc(func() { close(gate) })
}

func (r *spareRuntime) Start(ctx context.Context, id runtime.ID) error {
	r.cacheMu.Lock()
	held := r.startHeld
	c, err := r.get(id)
	if held == nil || err != nil || !c.spec.CacheUnpublished {
		r.cacheMu.Unlock()
		return r.freezeRuntime.Start(ctx, id)
	}
	r.startHeld, r.heldVM = nil, id
	r.cacheMu.Unlock()
	close(held)
	<-ctx.Done()
	return ctx.Err()
}

func TestOnlyALaunchASpareCanStandInForLeavesOne(t *testing.T) {
	t.Parallel()
	claude, _ := harness.Lookup("claude")
	eligible := func() *domain.Run {
		return &domain.Run{MemberID: "m", AccountMemberID: "m", Mode: domain.LaunchACP, ACP: true}
	}
	if !spareable(eligible(), domain.LaunchOptions{}, claude) {
		t.Fatal("an Enhanced Claude Code launch on the member's own login leaves no spare")
	}
	codex, _ := harness.Lookup("codex")
	effort := claude
	effort.ACPEffortArgs = true
	for name, c := range map[string]struct {
		run     func(*domain.Run)
		opts    domain.LaunchOptions
		profile harness.Profile
	}{
		"Standard":                          {run: func(r *domain.Run) { r.Mode, r.ACP = domain.LaunchTUI, false }, profile: claude},
		"Background":                        {run: func(r *domain.Run) { r.Mode = domain.LaunchHeadless }, profile: claude},
		"subsession":                        {opts: domain.LaunchOptions{ReservedRunID: "run-child"}, profile: claude},
		"from a session":                    {opts: domain.LaunchOptions{FromCommit: testBaseCommit}, profile: claude},
		"extra repository":                  {run: func(r *domain.Run) { r.Extras = []domain.RunRepo{{}} }, profile: claude},
		"added login":                       {run: func(r *domain.Run) { r.AccountLogin = "work" }, profile: claude},
		"login billed per use":              {run: func(r *domain.Run) { r.AccountLoginPerUse = true }, profile: claude},
		"shared account":                    {run: func(r *domain.Run) { r.AccountMemberID = "other" }, profile: claude},
		"requested account":                 {run: func(r *domain.Run) { r.RequestedAccountMemberID = "other" }, profile: claude},
		"Linear issue":                      {run: func(r *domain.Run) { r.LinearIssueURL = "https://linear.app/acme/issue/ENG-1" }, profile: claude},
		"connector":                         {run: func(r *domain.Run) { r.Via = "chatgpt" }, profile: claude},
		"instructions as the first message": {profile: codex},
		"effort set at the agent's start":   {profile: effort},
	} {
		run := eligible()
		if c.run != nil {
			c.run(run)
		}
		if spareable(run, c.opts, c.profile) {
			t.Errorf("%s: the launch leaves a spare", name)
		}
	}
}

func TestNoSpareIsMadeWithoutAnEligibleLaunchOrARuntimeThatKeepsItsCache(t *testing.T) {
	t.Parallel()
	// The spare's start begins, if at all, before the launch's open is over.
	settled := func(t *testing.T, e *testEnv, run domain.RunID) {
		t.Helper()
		e.opened(t, run)
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		if m := e.sched.members[e.member.ID]; m.spare != nil {
			t.Fatalf("the launch left a spare: %+v", m.spare)
		}
	}
	t.Run("an agent that reads its instructions as a message", func(t *testing.T) {
		t.Parallel()
		e, _ := newSpareEnv(t)
		settled(t, e, e.launchACP(t, "say pong").ID)
	})
	t.Run("a schedule's launch", func(t *testing.T) {
		t.Parallel()
		e, _ := newSpareEnv(t)
		settled(t, e, e.launchClaude(t, "say pong", domain.LaunchOptions{Scheduled: true}).ID)
	})
	t.Run("a runtime that publishes every cache", func(t *testing.T) {
		t.Parallel()
		e, _ := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")), func(cfg *Config) {
			cfg.Store = storetest.Unscoped{DB: cfg.Store.(storetest.Unscoped).WithSpares()}
		})
		withCoordination(t, e)
		settled(t, e, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	})
	t.Run("a launch that failed", func(t *testing.T) {
		t.Parallel()
		e, _ := newSpareEnv(t)
		e.rt.startErr = errors.New("no VM today")
		if _, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{}); err == nil {
			t.Fatal("the launch succeeded")
		}
		e.rt.startErr = nil
		e.waitNoSpares(t)
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		if m := e.sched.members[e.member.ID]; m.target != nil {
			t.Fatalf("a failed launch is the member's target: %+v", m.target)
		}
	})
}

// A launch the spare cannot stand in for removes it, with the branch name
// it reserved, and runs as a launch always did.
func TestALaunchTheSpareDiffersFromRemovesItAndRunsCold(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(t *testing.T, e *testEnv) domain.WorkspaceID{
		"another workspace": func(t *testing.T, e *testEnv) domain.WorkspaceID {
			other := &domain.Workspace{Name: "other", BaseBranch: "main"}
			if err := e.db.CreateWorkspace(t.Context(), other); err != nil {
				t.Fatal(err)
			}
			return other.ID
		},
		"the base moved": func(_ *testing.T, e *testEnv) domain.WorkspaceID {
			e.base.mu.Lock()
			defer e.base.mu.Unlock()
			e.base.result.Commit = "fedcba9876543210fedcba9876543210fedcba98"
			return e.ws.ID
		},
		"the sandbox changed": func(t *testing.T, e *testEnv) domain.WorkspaceID {
			if err := e.db.SetWorkspaceEnvironment(t.Context(), e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"WS": "2"}}); err != nil {
				t.Fatal(err)
			}
			return e.ws.ID
		},
		"the member's profile changed": func(t *testing.T, e *testEnv) domain.WorkspaceID {
			if _, err := e.cfg.Profiles.(*profile.Service).Put(t.Context(), string(e.member.ID), "claude", []profile.File{{Path: "settings.json", Mode: 0o600, Content: []byte("{}")}}); err != nil {
				t.Fatal(err)
			}
			return e.ws.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, rt := newSpareEnv(t)
			e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
			spare := e.waitSpare(t)
			row, err := e.sched.cfg.Store.GetRun(t.Context(), spare)
			if err != nil || row.Branch == "" {
				t.Fatalf("the spare's row = %+v, %v", row, err)
			}
			branch := row.Branch
			cid := e.cid(t, spare)
			ws := change(t, e)
			run, err := e.sched.LaunchWithOptions(t.Context(), ws, e.member.ID, e.member.ID, "say pong again", "claude", domain.LaunchACP, domain.LaunchOptions{})
			if err != nil {
				t.Fatalf("the launch after a miss: %v", err)
			}
			if run.ID == spare {
				t.Fatal("the launch claimed a spare that differs")
			}
			waitItems(t, e.sched, run.ID, "the cold launch's turn", turnEnded("end_turn", 1))
			waitFor(t, "the missed spare to go", func() bool {
				_, err := e.sched.cfg.Store.GetRun(t.Context(), spare)
				return errors.Is(err, store.ErrNotFound) && slices.Contains(e.git.releasedBranches(), branch)
			})
			if _, err := rt.get(cid); !errors.Is(err, runtime.ErrNotFound) {
				t.Fatalf("the missed spare's sandbox: %v, want it gone", err)
			}
			if len(rt.published) != 0 {
				t.Fatalf("published the cache of %v", rt.published)
			}
		})
	}
}

func TestTwoLaunchesAtOnceNeverClaimTheSpareTwice(t *testing.T) {
	t.Parallel()
	e, _ := newSpareEnv(t)
	e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	spare := e.waitSpare(t)
	var wg sync.WaitGroup
	made := make([]domain.RunID, 2)
	for i := range made {
		wg.Go(func() {
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong again", "claude", domain.LaunchACP, domain.LaunchOptions{})
			if err != nil {
				t.Errorf("launch %d: %v", i, err)
				return
			}
			made[i] = run.ID
		})
	}
	wg.Wait()
	// The launch that takes the spare misses it when the other holds the
	// member's cache lock at that moment, and then both run cold.
	if made[0] == "" || made[0] == made[1] {
		t.Fatalf("two launches made %v; want two sessions, the spare %s at most once", made, spare)
	}
	for _, id := range made {
		waitItems(t, e.sched, id, "each launch's turn", turnEnded("end_turn", 1))
	}
}

// A spare that is still starting holds its member's cache lock. A launch of
// theirs cancels it and goes on as soon as the spare's start returns, not
// when its VM is gone.
func TestALaunchCancelsAStartingSpareAndDoesNotWaitForItsRemoval(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	started, release := rt.holdSpareStart()
	defer release()
	e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	<-started
	run := e.launchClaude(t, "say pong again", domain.LaunchOptions{})
	rt.cacheMu.Lock()
	held := rt.heldVM
	rt.cacheMu.Unlock()
	if _, err := rt.get(held); err != nil {
		t.Fatalf("the launch returned after the cancelled spare's VM was gone: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("the launch = %+v", run)
	}
	release()
	waitFor(t, "the cancelled spare's VM to go", func() bool {
		_, err := rt.get(held)
		return errors.Is(err, runtime.ErrNotFound)
	})
	// The launch leaves a spare of its own, and nothing of the cancelled one.
	next := e.waitSpare(t)
	spares, err := e.sched.cfg.Store.ListSpareRuns(t.Context())
	if err != nil || len(spares) != 1 || spares[0].ID != next {
		t.Fatalf("spare sessions = %v, %v; want only %s", runIDsOf(spares), err, next)
	}
}

func (r *spareRuntime) DiskUse(context.Context) (map[runtime.ID]int64, error) {
	return map[runtime.ID]int64{}, nil
}

func (r *spareRuntime) HoldDisks(_ context.Context, id runtime.ID) error {
	r.note("hold", id)
	return nil
}

// spareAndSession leaves the member an idle session whose VM holds 2 GiB
// and a ready spare whose VM holds 1 GiB, and returns their VMs.
func spareAndSession(t *testing.T, e *testEnv, rt *spareRuntime) (session, spare runtime.ID) {
	t.Helper()
	first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
	waitItems(t, e.sched, first.ID, "the first task's turn", turnEnded("end_turn", 1))
	waitFor(t, "the agent idle", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.liveFactsLocked(e.sched.runs[first.ID]).busy == ""
	})
	session, spare = e.cid(t, first.ID), e.cid(t, e.waitSpare(t))
	rt.memMu.Lock()
	rt.vm[session], rt.vm[spare] = 2<<30, 1<<30
	rt.memMu.Unlock()
	return session, spare
}

const testFloor = 32 << 30 / 10

// Whatever runs short, the spares go before anything is taken from a
// member's session.
func TestSparesGoFirstUnderPressure(t *testing.T) {
	t.Parallel()
	t.Run("a launch short of memory", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t)
		_, spare := spareAndSession(t, e, rt)
		// The spare's 1 GiB is enough: nothing is saved.
		rt.setAvailable(testFloor + 256<<20)
		e.launchACP(t, "say pong")
		if got := rt.did(); !slices.Equal(got, []string{"destroy " + string(spare)}) {
			t.Fatalf("a launch short of memory did %q, want only the spare removed", got)
		}
	})
	t.Run("a launch still short without the spare", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t)
		session, spare := spareAndSession(t, e, rt)
		rt.setAvailable(testFloor - 700<<20)
		e.launchACP(t, "say pong")
		if got := rt.did(); !slices.Equal(got, []string{"destroy " + string(spare), "save " + string(session)}) {
			t.Fatalf("a launch short of memory did %q, want the spare removed, then the idle session saved", got)
		}
	})
	t.Run("the memory guard", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t)
		session, spare := spareAndSession(t, e, rt)
		rt.setAvailable(testFloor - 2<<30)
		e.sched.guardVMMemory(t.Context())
		e.waitNoSpares(t)
		if got := rt.did(); !slices.Equal(got, []string{"destroy " + string(spare)}) {
			t.Fatalf("the guard's first check did %q, want only the spare removed", got)
		}
		e.sched.guardVMMemory(t.Context())
		if got := rt.did(); got[0] != "destroy "+string(spare) || !slices.Contains(got, "save "+string(session)) {
			t.Fatalf("the guard's next check did %q, want the idle session saved after the spare went", got)
		}
	})
	t.Run("the memory brake", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t)
		session, spare := spareAndSession(t, e, rt)
		// A check above the floor lists what the next may freeze.
		e.brake(rt.freezeRuntime, testFloor+1<<30)
		rt.setAvailable(testFloor - 2<<30)
		e.sched.brakeVMMemory(t.Context(), rt)
		if frozen := rt.Frozen(); len(frozen) != 0 {
			t.Fatalf("the brake froze %v on the check that found a spare", frozen)
		}
		e.waitNoSpares(t)
		rt.setAvailable(testFloor - 2<<30)
		e.sched.brakeVMMemory(t.Context(), rt)
		if frozen := rt.Frozen(); !slices.Equal(frozen, []runtime.ID{session}) {
			t.Fatalf("the brake's next check froze %v, want the session %s", frozen, session)
		}
		if slices.Contains(rt.did(), "freeze "+string(spare)) {
			t.Fatal("the brake froze a spare's VM")
		}
	})
	t.Run("disk reclaim", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
		_, spare := spareAndSession(t, e, rt)
		closed := closeRetained(t, e, "closed session")
		e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			if _, err := rt.get(spare); err != nil {
				return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 80 << 30}, nil
			}
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 1 << 30}, nil
		}
		e.sched.reclaimDisk(t.Context(), "")
		if _, err := rt.get(spare); !errors.Is(err, runtime.ErrNotFound) {
			t.Fatalf("the spare's VM after a reclaim below the floor: %v, want it gone", err)
		}
		if e.rt.byName(string(closed.ID)) == nil {
			t.Fatal("reclaim removed a closed session's container while a spare held the disk")
		}
	})
	t.Run("the disk guard", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t, func(cfg *Config) {
			cfg.MinFreeBytes = 10 << 30
		})
		_, spare := spareAndSession(t, e, rt)
		e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 1 << 30}, nil
		}
		e.sched.guardVMDisks(t.Context())
		e.waitNoSpares(t)
		if got := rt.did(); !slices.Equal(got, []string{"destroy " + string(spare)}) {
			t.Fatalf("the disk guard's first check did %q, want only the spare removed", got)
		}
	})
}

// A spare is made only from room nobody needs, and frees nothing to get it.
func TestNoSpareIsMadeIntoRoomASessionNeeds(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	// Above the floor a launch needs, below the guard's calm line plus a spare.
	rt.setAvailable(2*testFloor + spareMemoryBytes - 1)
	first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
	e.opened(t, first.ID)
	e.waitNoSpares(t)
	if got := rt.did(); len(got) != 0 {
		t.Fatalf("making a spare did %q, want nothing freed", got)
	}
	e.sched.mu.Lock()
	sessions := len(e.sched.runs)
	e.sched.mu.Unlock()
	if sessions != 1 {
		t.Fatalf("%d sessions run, want the launched one alone", sessions)
	}
	// With the room, the next launch leaves one.
	rt.setAvailable(2*testFloor + spareMemoryBytes)
	e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	e.waitSpare(t)
}

func TestASpareNobodyClaimsIsRemovedAfterTheWindow(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	spare := e.waitSpare(t)
	row, err := e.sched.cfg.Store.GetRun(t.Context(), spare)
	if err != nil {
		t.Fatal(err)
	}
	cid := e.cid(t, spare)
	e.sched.sweepSpares()
	if e.spare() != spare {
		t.Fatal("the sweep removed a spare inside the window")
	}
	e.sched.mu.Lock()
	e.sched.members[e.member.ID].last = time.Now().Add(-spareWindow)
	e.sched.mu.Unlock()
	e.sched.sweepSpares()
	e.waitNoSpares(t)
	if _, err := rt.get(cid); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("the old spare's VM: %v, want it gone", err)
	}
	if released := e.git.releasedBranches(); !slices.Equal(released, []string{row.Branch}) {
		t.Fatalf("released branches = %v, want %s", released, row.Branch)
	}
	if _, err := os.Stat(e.git.checkoutPath(spare)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old spare's checkout: %v, want it gone", err)
	}
}

// A spare is never taken up again by the next server: its member's next
// launch may no longer match it.
func TestARestartRemovesTheSpareTheLastServerLeft(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
	waitItems(t, e.sched, first.ID, "the first task's turn", turnEnded("end_turn", 1))
	spare := e.waitSpare(t)
	row, err := e.sched.cfg.Store.GetRun(t.Context(), spare)
	if err != nil {
		t.Fatal(err)
	}
	cid := e.cid(t, spare)
	sub := e.subscribe(t)
	next := e.restartACP(t)
	waitFor(t, "the leftover spare to go", func() bool {
		spares, err := next.cfg.Store.ListSpareRuns(t.Context())
		_, vmErr := rt.get(cid)
		return err == nil && len(spares) == 0 && errors.Is(vmErr, runtime.ErrNotFound)
	})
	if released := e.git.releasedBranches(); !slices.Equal(released, []string{row.Branch}) {
		t.Fatalf("released branches = %v, want %s", released, row.Branch)
	}
	for len(sub.Events()) > 0 {
		if ev := <-sub.Events(); ev.RunID == spare {
			t.Fatalf("the restart published %+v about the spare", ev)
		}
	}
	waitFor(t, "the member's session adopted", func() bool { return next.acp.session(first.ID) != nil })
}

func TestASpareIsDroppedForItsMemberAndItsWorkspace(t *testing.T) {
	t.Parallel()
	t.Run("member", func(t *testing.T) {
		t.Parallel()
		e, rt := newSpareEnv(t)
		capture := newSchedulerEvidenceCapture(e.ws.ID)
		e.sched.UseEvidence(capture)
		e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
		spare := e.waitSpare(t)
		cid := e.cid(t, spare)
		e.sched.DropSpares(t.Context(), e.member.ID, true)
		if _, err := rt.get(cid); !errors.Is(err, runtime.ErrNotFound) {
			t.Fatalf("the dropped spare's VM: %v, want it gone", err)
		}
		if _, err := e.sched.cfg.Store.GetRun(t.Context(), spare); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the dropped spare's row: %v, want it gone", err)
		}
		capture.mu.Lock()
		defer capture.mu.Unlock()
		if len(capture.reqs) != 0 {
			t.Fatalf("a spare recorded evidence: %+v", capture.reqs)
		}
	})
	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		e, _ := newSpareEnv(t)
		first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
		e.opened(t, first.ID)
		e.waitSpare(t)
		if err := e.sched.Kill(t.Context(), first.ID, e.member.ID); err != nil {
			t.Fatal(err)
		}
		e.waitTerminal(t, first.ID)
		var seen []domain.RunID
		if err := e.sched.WithInactiveWorkspace(t.Context(), e.ws.ID, func(runs []*domain.Run) error {
			seen = runIDsOf(runs)
			return nil
		}); err != nil {
			t.Fatalf("deleting a workspace with a spare in it: %v", err)
		}
		if !slices.Equal(seen, []domain.RunID{first.ID}) {
			t.Fatalf("the workspace's sessions at its deletion = %v, want the member's alone", seen)
		}
		e.waitNoSpares(t)
	})
}

// failingStatusStore fails a run's change to running while failing is set.
type failingStatusStore struct {
	store.Store
	failing atomic.Bool
}

func (s *failingStatusStore) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, activity domain.RunActivity, startedAt, finishedAt *time.Time) (store.RunStatusStamp, error) {
	if s.failing.Load() && status == domain.RunRunning {
		return store.RunStatusStamp{}, errors.New("disk I/O error")
	}
	return s.Store.UpdateRunStatus(ctx, id, status, reason, activity, startedAt, finishedAt)
}

// Once the spare's row is the member's, the launch is that session: what
// fails then fails the session, and no second one is started.
func TestAClaimThatFailsAfterTheRowIsClaimedFailsTheSession(t *testing.T) {
	t.Parallel()
	var failing *failingStatusStore
	e, _ := newSpareEnv(t, func(cfg *Config) {
		failing = &failingStatusStore{Store: cfg.Store}
		cfg.Store = failing
	})
	first := e.launchClaude(t, "say pong", domain.LaunchOptions{})
	e.opened(t, first.ID)
	spare := e.waitSpare(t)
	failing.failing.Store(true)
	_, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong again", "claude", domain.LaunchACP, domain.LaunchOptions{})
	failing.failing.Store(false)
	if err == nil || !strings.Contains(err.Error(), "session "+string(spare)+" failed") {
		t.Fatalf("the launch = %v, want the claimed session's failure", err)
	}
	row := e.waitTerminal(t, spare)
	if row.Task != "say pong again" || row.Spare {
		t.Fatalf("the failed session = %+v, want the launch's own", row)
	}
	runs, err := e.db.ListRunsByMember(t.Context(), e.member.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("the member's sessions = %v, %v; want the first and the failed one", runIDsOf(runs), err)
	}
}

// A server that stops after the spare's row is claimed and before its agent
// has the task leaves a session that still owes it: the next server sends
// the task to a new agent session.
func TestARestartInTheMiddleOfAClaimStillDeliversTheTask(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	e.opened(t, e.launchClaude(t, "say pong", domain.LaunchOptions{}).ID)
	spare := e.waitSpare(t)
	st := e.sched.cfg.Store
	e.sched.mu.Lock()
	entry := e.sched.runs[spare]
	entry.agentSessionID = ""
	err := e.sched.writeSidecar(entry.sidecar())
	e.sched.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.GetRun(t.Context(), spare)
	if err != nil {
		t.Fatal(err)
	}
	row.Task = "say pong again"
	if err = st.ClaimSpareRun(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	for _, to := range []domain.RunStatus{domain.RunProvisioning, domain.RunRunning} {
		if _, err = st.UpdateRunStatus(t.Context(), spare, to, "", "", &started, nil); err != nil {
			t.Fatal(err)
		}
	}
	agents := len(rt.all())
	next := e.restartACP(t)
	waitItems(t, next, spare, "the task's turn after the restart", turnEnded("end_turn", 1))
	execs := rt.all()
	if len(execs) != agents+1 {
		t.Fatalf("the restart started %d agents, want one for the session that owed its task", len(execs)-agents)
	}
	if prompts := execs[len(execs)-1].wire.prompts(t); len(prompts) != 1 || !slices.Equal(textOf(prompts[0]), []string{"say pong again"}) {
		t.Fatalf("the new agent was prompted %v, want the launch's task", prompts)
	}
}

// A launch with no task claims the spare too, and the session reads as a
// cold launch with no task does: its agent waits for the first message.
func TestAClaimWithoutATaskReadsAsAColdLaunchWithoutOne(t *testing.T) {
	t.Parallel()
	e, rt := newSpareEnv(t)
	cold := e.launchClaude(t, "", domain.LaunchOptions{})
	e.opened(t, cold.ID)
	spare := e.waitSpare(t)
	agents := len(rt.all())
	claimed := e.launchClaude(t, "", domain.LaunchOptions{})
	if claimed.ID != spare || len(rt.all()) != agents {
		t.Fatalf("the launch made %s with %d more agents, want the spare %s and none", claimed.ID, len(rt.all())-agents, spare)
	}
	standing := func(id domain.RunID) string {
		row, err := e.db.GetRun(t.Context(), id)
		if err != nil {
			return err.Error()
		}
		return string(row.Status) + " " + row.Reason + " " + string(row.Activity)
	}
	waitFor(t, "the claimed session to read as the cold one", func() bool { return standing(spare) == standing(cold.ID) })
	if prompts := rt.all()[agents-1].wire.prompts(t); len(prompts) != 0 {
		t.Fatalf("a claim with no task prompted the agent: %v", prompts)
	}
}
