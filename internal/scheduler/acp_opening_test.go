package scheduler

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// launchOpening launches an Enhanced run whose agent does not start until
// open is called with what its start returns.
func (e *testEnv) launchOpening(t *testing.T, rt *acpRuntime, task string) (run *domain.Run, open func(error)) {
	t.Helper()
	gate := make(chan error, 1)
	t.Cleanup(func() { close(gate) })
	rt.onAttach = func() error { return <-gate }
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run, func(err error) { gate <- err }
}

// holdPrompt sends text to a run whose agent is still starting and returns
// where what became of it arrives.
func (e *testEnv) holdPrompt(t *testing.T, run domain.RunID, text string) <-chan error {
	t.Helper()
	delivered := make(chan error, 1)
	outcome, err := e.sched.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: text}, false, func(err error) { delivered <- err })
	if err != nil || outcome != acphost.OutcomeQueued {
		t.Fatalf("Inject(%q) = %q, %v; want it held as queued", text, outcome, err)
	}
	return delivered
}

func TestEnhancedLaunchReturnsBeforeItsAgentStartsAndHoldsPrompts(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "the task")
	if run.Status != domain.RunRunning {
		t.Fatalf("launched run is %s, want running", run.Status)
	}
	stream, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	if !stream.Starting || stream.Items != nil {
		t.Fatalf("stream starting %t, live %t; want a starting session", stream.Starting, stream.Items != nil)
	}
	first := e.holdPrompt(t, run.ID, "first")
	second := e.holdPrompt(t, run.ID, "second")
	select {
	case err := <-first:
		t.Fatalf("the held prompt settled before the agent started: %v", err)
	default:
	}

	open(nil)
	for _, delivered := range []<-chan error{first, second} {
		if err := <-delivered; err != nil {
			t.Fatalf("held prompt: %v", err)
		}
	}
	<-stream.Started
	waitItems(t, e.sched, run.ID, "three turns", turnEnded("end_turn", 3))
	var got []string
	for _, prompt := range rt.all()[0].wire.prompts(t) {
		got = append(got, textOf(prompt)[0])
	}
	if want := []string{"the task", "first", "second"}; !slices.Equal(got, want) {
		t.Fatalf("the agent got %q, want %q", got, want)
	}
}

func TestEnhancedLaunchReturnsAHeldPromptWithWhyItsAgentDidNotStart(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "")
	stream, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	delivered := e.holdPrompt(t, run.ID, "first")

	// A keeper that cannot be attached falls back to an adapter without one.
	rt.mu.Lock()
	rt.startErr = errors.New("acpRuntime: the agent is not installed")
	rt.mu.Unlock()
	open(errors.New("acpRuntime: the bridge failed"))
	if err := <-delivered; !errors.Is(err, ErrACPNotRunning) || !errors.Is(err, ptyhost.ErrNoSession) || !strings.Contains(err.Error(), "the agent is not installed") {
		t.Fatalf("held prompt settled with %v, want the agent's own failure", err)
	}
	<-stream.Started
	if again, err := e.sched.ACPSubscribe(run.ID, 0); err != nil || again.Starting {
		t.Fatalf("stream after the failed start: starting %t, %v", again.Starting, err)
	} else {
		again.Cancel()
	}
}

func TestEnhancedRunKilledWhileItsAgentStartsEndsWithoutASession(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "")
	delivered := e.holdPrompt(t, run.ID, "first")
	if err := e.sched.Kill(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	open(nil)
	<-delivered
	e.waitTerminal(t, run.ID)
	waitFor(t, "the agent's session to stop", func() bool { return e.sched.acp.session(run.ID) == nil })
}

func TestCloseWaitsForAnAgentStillStarting(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "")
	time.AfterFunc(100*time.Millisecond, func() { open(nil) })
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	if e.sched.acp.starting(run.ID) {
		t.Fatal("Close returned while the launch's open was still running")
	}
}

func TestEnhancedRunARestartCaughtStartingGetsItsTask(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	rt.startErr = errors.New("acpRuntime: the server stopped")
	run := e.launchACP(t, "the task")
	rt.mu.Lock()
	rt.startErr = nil
	rt.mu.Unlock()

	s := e.restart(t, e.sched)
	waitItems(t, s, run.ID, "the task's turn", turnEnded("end_turn", 1))
	execs := rt.all()
	if got := textOf(execs[len(execs)-1].wire.prompts(t)[0]); !slices.Equal(got, []string{"the task"}) {
		t.Fatalf("the agent got %q, want the task", got)
	}
}

func TestTheMemoryBrakeLeavesASessionWhoseAgentStartsRunning(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	run, open := e.launchOpening(t, fr.acpRuntime, "")
	cid := e.cid(t, run.ID)
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)
	if frozen := fr.Frozen(); len(frozen) != 0 {
		t.Fatalf("the brake froze %v while its agent started", frozen)
	}
	open(nil)
	e.opened(t, run.ID)
	e.brake(fr, 3<<30)
	e.brake(fr, 3<<30)
	if frozen := fr.Frozen(); !slices.Equal(frozen, []runtime.ID{cid}) {
		t.Fatalf("frozen = %v, want the session once its agent has started", frozen)
	}
}

func TestALaunchKeepsItsMemoryClaimUntilItsAgentHasStarted(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	claims := func() int {
		e.sched.capacityGate <- struct{}{}
		defer func() { <-e.sched.capacityGate }()
		return len(e.sched.memoryClaims)
	}
	run, open := e.launchOpening(t, rt, "")
	if got := claims(); got != 1 {
		t.Fatalf("%d memory claims while the agent starts, want the launch's", got)
	}
	open(nil)
	e.opened(t, run.ID)
	waitFor(t, "the launch's memory claim to end", func() bool { return claims() == 0 })
}

func TestEnhancedRunPausedAndResumedWhileItsAgentStartsKeepsItsTask(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "the task")
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan error, 1)
	go func() { resumed <- e.sched.Resume(t.Context(), run.ID, e.member.ID) }()
	waitFor(t, "the resume to reach the agent", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].resuming
	})
	open(nil)
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}
	items := waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if execs := rt.all(); len(execs) != 1 || interrupted(items) {
		t.Fatalf("the resume started %d agents, task interrupted %t; want the one the launch started", len(execs), interrupted(items))
	}
}

func TestEnhancedRunCancelAndAnswerDoNotWaitForAnAgentStillStarting(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, open := e.launchOpening(t, rt, "")
	defer open(nil)
	if err := e.sched.ACPCancel(t.Context(), run.ID); !errors.Is(err, ErrACPNotRunning) || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("ACPCancel while the agent starts = %v", err)
	}
	if err := e.sched.ACPAnswer(run.ID, "request", "allow", nil); !errors.Is(err, ErrACPNotRunning) || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("ACPAnswer while the agent starts = %v", err)
	}
}

// A server killed between session/new and the task leaves a keeper that
// holds a session the run never recorded.
func TestEnhancedRunAKillCaughtStartingStartsItsAgentAgainWithItsTask(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "the task")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc.AgentSessionID = ""
	if err = e.sched.writeSidecar(sc); err != nil {
		t.Fatal(err)
	}
	if err = e.db.SetRunAgentSession(t.Context(), run.ID, ""); err != nil {
		t.Fatal(err)
	}

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	startScheduler(t, s)
	items := waitItems(t, s, run.ID, "the task's second turn", turnEnded("end_turn", 2))
	execs := rt.all()
	if len(execs) != 2 || !execs[0].exited() {
		t.Fatalf("%d agents, the first exited %t; want the unfinished start replaced", len(execs), len(execs) > 0 && execs[0].exited())
	}
	if got := textOf(execs[1].wire.prompts(t)[0]); !slices.Equal(got, []string{"the task"}) {
		t.Fatalf("the new agent got %q, want the task", got)
	}
	if hasNotice("Agent restarted")(items) {
		t.Fatal("the start was reported as a restored session")
	}
}
