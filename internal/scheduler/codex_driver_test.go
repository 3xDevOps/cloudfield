package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/codexapp"
	"github.com/3xDevOps/cloudfield/internal/codexapp/codexmock"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// codexRuntime serves every pipe exec, the app-server bridge, from its
// container's fake app-server.
type codexRuntime struct {
	*fakeRuntime

	mu       sync.Mutex
	servers  map[runtime.ID]*codexmock.Server
	execs    []*acpExec
	startErr error
}

func (r *codexRuntime) StartExecTTY(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("codexRuntime: no terminal execs")
}

func (r *codexRuntime) StartExecPipe(_ context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return nil, r.startErr
	}
	serverIn, hostOut := io.Pipe()
	hostIn, serverOut := io.Pipe()
	e := &acpExec{
		identity: runtime.ExecIdentity{ContainerID: id, ExecID: rand.Text(), CreationKey: spec.CreationKey, ClaimToken: "claim"},
		argv:     spec.Argv,
		stdin:    hostOut, stdout: hostIn, agentIn: serverIn, agentOut: serverOut,
		done: make(chan struct{}),
	}
	srv := r.servers[id]
	if srv == nil {
		srv = codexmock.New()
		r.servers[id] = srv
	}
	go func() {
		srv.Serve(serverIn, serverOut)
		_ = serverOut.Close()
		close(e.done)
	}()
	r.execs = append(r.execs, e)
	return e, nil
}

func (r *codexRuntime) RecoverExec(context.Context, runtime.ExecIdentity) (runtime.ManagedExec, error) {
	return nil, runtime.ErrExecUnavailable
}

// bridge waits for the nth bridge exec, counting from 1.
func (r *codexRuntime) bridge(t *testing.T, n int) (*codexmock.Server, *acpExec) {
	t.Helper()
	waitFor(t, "the app-server bridge", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.execs) >= n
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.execs[n-1]
	return r.servers[e.identity.ContainerID], e
}

// dialed waits until n clients have connected to srv.
func dialed(t *testing.T, srv *codexmock.Server, n int) {
	t.Helper()
	waitFor(t, "the client to connect", func() bool { return len(srv.Requests("thread/loaded/list")) >= n })
}

func newCodexEnv(t *testing.T) (*testEnv, *codexRuntime, *fakeCoordinator) {
	t.Helper()
	rt := &codexRuntime{fakeRuntime: newFakeRuntime(), servers: make(map[runtime.ID]*codexmock.Server)}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.ServerBinary = fakeServerBinary(t, "#!/bin/sh\necho aether\n")
		cfg.Runtime = rt
	})
	e.rt = rt.fakeRuntime
	coord, _ := withCoordination(t, e)
	return e, rt, coord
}

func TestStandardCodexRunsThroughItsAppServer(t *testing.T) {
	t.Parallel()
	e, rt, coord := newCodexEnv(t)
	run, _ := e.launchOn(t, "codex")
	srv, bridge := rt.bridge(t, 1)
	if !slices.Equal(bridge.argv, codexapp.BridgeCommand) {
		t.Fatalf("bridge argv = %v, want %v", bridge.argv, codexapp.BridgeCommand)
	}
	dialed(t, srv, 1)

	srv.StartThread("title", true, "")
	srv.StartThread("thread-1", false, "")
	srv.SetStatus("title", "active")
	srv.SetStatus("thread-1", "active")
	srv.SetStatus("thread-1", "idle")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if reads := srv.Requests("thread/turns/list"); len(reads) != 0 {
		t.Fatalf("thread/turns/list = %s, want no read of a session with no parent", reads)
	}
	waitFor(t, "the recorded session", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.HarnessSessionID == "thread-1"
	})
	coord.mu.Lock()
	opened := slices.Clone(coord.opened)
	coord.mu.Unlock()
	if !slices.Equal(opened, []domain.RunID{run.ID}) {
		t.Fatalf("sessions opened = %v, want the swarm baseline taken once for %s", opened, run.ID)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "also add a test"}, false, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if !e.sched.Wakeable(run.ID) || !e.sched.InTurn(run.ID) {
		t.Fatal("a connected Standard Codex session is not wakeable mid-turn")
	}
	if err := e.sched.Wake(t.Context(), run.ID, "Aether has 1 unacknowledged inbox item(s)."); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if got := promptTexts(t, srv.Requests("turn/start")); !slices.Equal(got, []string{"also add a test"}) {
		t.Fatalf("turn/start inputs = %q", got)
	}
	if got := promptTexts(t, srv.Requests("turn/steer")); len(got) != 1 || !strings.HasPrefix(got[0], "Aether has 1") {
		t.Fatalf("turn/steer inputs = %q, want the mail notice", got)
	}
	e.pty.mu.Lock()
	injects, annotations := len(e.pty.injects), slices.Clone(e.pty.annotations)
	e.pty.mu.Unlock()
	if injects != 0 || len(annotations) != 1 || annotations[0].name != e.member.DisplayName || annotations[0].message != "also add a test" {
		t.Fatalf("terminal injects = %d, annotations = %+v; want the message shown, not typed", injects, annotations)
	}

	if err := e.sched.Kill(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitFor(t, "the bridge to stop", bridge.exited)
}

// A restarted server reattaches to the app-server its terminal kept, and
// follows the session the run recorded.
func TestRecoveredStandardCodexReattachesToItsAppServer(t *testing.T) {
	t.Parallel()
	e, rt, _ := newCodexEnv(t)
	run, _ := e.launchOn(t, "codex")
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	srv.SetStatus("thread-1", "active")
	srv.SetStatus("thread-1", "idle")
	waitFor(t, "the recorded session", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.HarnessSessionID == "thread-1"
	})
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The terminal opened a newer thread the old server never saw.
	srv.LoadThread("thread-2", false, "", 9)

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	rt.bridge(t, 2)
	dialed(t, srv, 2)
	waitFor(t, "the reattached client", func() bool { return s2.Wakeable(run.ID) })
	if err := s2.Wake(t.Context(), run.ID, "mail"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	var start struct {
		ThreadID string `json:"threadId"`
	}
	starts := srv.Requests("turn/start")
	if len(starts) != 1 || json.Unmarshal(starts[0], &start) != nil || start.ThreadID != "thread-1" {
		t.Fatalf("turn/start = %s, want one on the recorded thread-1", starts)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

// Mail a terminal's turn takes while the run shows a question does not
// answer it; the member's own message does.
func TestStandardCodexKeepsAQuestionThroughAMailTurn(t *testing.T) {
	t.Parallel()
	e, rt, _ := newCodexEnv(t)
	run, _ := e.launchOn(t, "codex")
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	srv.SetStatus("thread-1", "active")
	srv.SetStatus("thread-1", "idle")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.ReportNeedsInput(t.Context(), run.ID, "needs-input-1", "which region?", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == heldQuestion
	})

	if err := e.sched.Wake(t.Context(), run.ID, "unread mail"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the mail turn's working report", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].agentReport.State == agentstatus.Working
	})
	srv.EndTurn("thread-1", "completed")
	waitFor(t, "the mail turn's end", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].agentReport.State == agentstatus.Idle
	})
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunNeedsAttention || r.Reason != heldQuestion {
		t.Fatalf("run = %+v, %v; want the question kept through the mail turn", r, err)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "us-east"}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if reason := e.idleReason(run.ID); reason != "" {
		t.Fatalf("idle reason %q after the answer reached Codex, want none", reason)
	}
}

// A Standard Codex subsession's finished turn forwards the last agent
// message of its thread. A turn cut off with Esc forwards nothing.
func TestStandardCodexChildForwardsItsLastMessage(t *testing.T) {
	shortProvisionalGrace(t)
	e, rt, coord := newCodexEnv(t)
	parent, _ := e.launchFake(t, "parent")
	child := launchChildRun(t, e, parent, "codex", domain.LaunchTUI)
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(child.ID) })
	prompt := func() {
		t.Helper()
		if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
			t.Fatal(err)
		}
		e.waitAgentWorking(t, child.ID)
	}
	prompt()
	srv.EndTurn("thread-1", "interrupted", "looking")
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
	time.Sleep(provisionalGrace * 3 / 2)
	if calls := coord.provisionalReports(); len(calls) != 0 {
		t.Fatalf("provisional reports %+v after a turn cut off, want none", calls)
	}
	prompt()
	srv.EndTurn("thread-1", "completed", "pong")
	waitFor(t, "the thread's last message at the parent", func() bool {
		calls := coord.provisionalReports()
		return len(calls) == 1 && calls[0].run == child.ID && calls[0].message == "pong"
	})
}

// A Standard Codex turn cut off with Esc is not asked for a report, nor is one
// with no agent message, as a !command or /compact lists. A turn the agent
// answered without a report is asked once, in a turn its app-server starts,
// and a parent gets the asked turn's message.
func TestStandardCodexTurnWithoutAReportIsAskedOnce(t *testing.T) {
	shortProvisionalGrace(t)
	e, rt, coord := newCodexEnv(t)
	coord.remind = e.sched.Wake
	parent, _ := e.launchFake(t, "parent")
	child := launchChildRun(t, e, parent, "codex", domain.LaunchTUI)
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(child.ID) })
	prompt := func() *domain.Run {
		t.Helper()
		if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
			t.Fatal(err)
		}
		e.waitAgentWorking(t, child.ID)
		return e.waitStoreStatus(t, child.ID, domain.RunRunning)
	}

	prompt()
	srv.EndTurn("thread-1", "interrupted", "looking")
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
	prompt()
	srv.EndTurn("thread-1", "completed")
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)

	working := prompt()
	srv.EndTurn("thread-1", "completed", "pong")
	waitFor(t, "the reminder's turn", func() bool { return len(srv.Requests("turn/start")) == 4 })
	if r := e.waitStoreStatus(t, child.ID, domain.RunRunning); r.StatusSeq != working.StatusSeq {
		t.Fatalf("status seq %d once reminded, want %d: the asked turn's end is not shown", r.StatusSeq, working.StatusSeq)
	}
	srv.EndTurn("thread-1", "completed", "reported")
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
	want := []string{"say pong", "say pong", "say pong", reminderPrefix + " (answered, waiting, needs-input, blocked, success or failure) before ending the turn."}
	if got := promptTexts(t, srv.Requests("turn/start")); !slices.Equal(got, want) {
		t.Fatalf("turn/start inputs = %q, want %q", got, want)
	}
	waitFor(t, "the asked turn's message at the parent", func() bool {
		calls := coord.provisionalReports()
		return len(calls) > 0 && calls[len(calls)-1].run == child.ID && calls[len(calls)-1].message == "pong"
	})
}

// The asked turn completed, so a parent gets its message even when the
// reminder's turn is cut off with Esc or fails.
func TestStandardCodexAskedTurnForwardsWhenItsReminderDoesNotFinish(t *testing.T) {
	shortProvisionalGrace(t)
	for name, end := range map[string]func(*codexmock.Server){
		"cut off": func(srv *codexmock.Server) { srv.EndTurn("thread-1", "interrupted") },
		"failed":  func(srv *codexmock.Server) { srv.SetStatus("thread-1", "systemError") },
	} {
		t.Run(name, func(t *testing.T) {
			e, rt, coord := newCodexEnv(t)
			coord.remind = e.sched.Wake
			parent, _ := e.launchFake(t, "parent")
			child := launchChildRun(t, e, parent, "codex", domain.LaunchTUI)
			srv, _ := rt.bridge(t, 1)
			dialed(t, srv, 1)
			srv.StartThread("thread-1", false, "")
			waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(child.ID) })
			if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
				t.Fatal(err)
			}
			e.waitAgentWorking(t, child.ID)
			srv.EndTurn("thread-1", "completed", "pong")
			waitFor(t, "the reminder's turn", func() bool { return len(srv.Requests("turn/start")) == 2 })
			end(srv)
			e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
			waitFor(t, "the asked turn's message at the parent", func() bool {
				calls := coord.provisionalReports()
				return len(calls) == 1 && calls[0].run == child.ID && calls[0].message == "pong"
			})
		})
	}
}

// A turn that starts before the reminder is sent, as a follow-up queued in the
// terminal does, is not asked about the turn before it: that turn's end is
// shown, and the new turn is asked for its own report at its end.
func TestStandardCodexReminderJoinsNoLaterTurn(t *testing.T) {
	shortProvisionalGrace(t)
	e, rt, coord := newCodexEnv(t)
	parent, _ := e.launchFake(t, "parent")
	child := launchChildRun(t, e, parent, "codex", domain.LaunchTUI)
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(child.ID) })
	var followUp sync.Once
	reminded := make(chan error, 2)
	coord.remind = func(ctx context.Context, run domain.RunID, reminder string) error {
		followUp.Do(func() {
			if _, err := e.sched.Inject(ctx, run, e.member.ID, domain.AgentPrompt{Text: "say ping"}, false, nil); err != nil {
				t.Errorf("Inject: %v", err)
			}
		})
		err := e.sched.Wake(ctx, run, reminder)
		reminded <- err
		return err
	}
	if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.waitAgentWorking(t, child.ID)

	srv.EndTurn("thread-1", "completed", "pong")
	waitFor(t, "the asked turn's reminder", func() bool { return len(reminded) == 1 })
	if err := <-reminded; err == nil {
		t.Fatalf("the reminder was sent into the follow-up's turn: turn/steer = %s", srv.Requests("turn/steer"))
	}
	srv.EndTurn("thread-1", "completed", "ping")
	waitFor(t, "the follow-up's reminder", func() bool { return len(reminded) == 1 })
	if err := <-reminded; err != nil {
		t.Fatalf("the follow-up's reminder: %v", err)
	}
	srv.EndTurn("thread-1", "completed", "reported")
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
	want := []string{"say pong", "say ping", reminderPrefix + " (answered, waiting, needs-input, blocked, success or failure) before ending the turn."}
	if got := promptTexts(t, srv.Requests("turn/start")); !slices.Equal(got, want) {
		t.Fatalf("turn/start inputs = %q, want %q", got, want)
	}
	waitFor(t, "the follow-up's message at the parent", func() bool {
		calls := coord.provisionalReports()
		return len(calls) > 0 && calls[len(calls)-1].run == child.ID && calls[len(calls)-1].message == "ping"
	})
}

// A restarted server finds a Standard Codex thread idle after a turn nobody
// reported. No turn ends there, so the session is not asked.
func TestRecoveredStandardCodexIsNotAskedForAnOldTurn(t *testing.T) {
	t.Parallel()
	e, rt, coord := newCodexEnv(t)
	run, _ := e.launchOn(t, "codex")
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(run.ID) })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.waitAgentWorking(t, run.ID)
	srv.EndTurn("thread-1", "completed", "pong")
	waitFor(t, "the turn's end", func() bool { return len(coord.idleWakeRuns()) == 2 })
	reads := len(srv.Requests("thread/turns/list"))
	binDir := e.sched.coordinationSeam().binDir
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.UseCoordination(coord, binDir)
	coord.remind = s2.Wake
	startScheduler(t, s2)
	waitFor(t, "the idle thread's report", func() bool { return len(coord.idleWakeRuns()) == 3 })
	if starts := srv.Requests("turn/start"); len(starts) != 1 {
		t.Fatalf("turn/start = %s, want the member's prompt only", starts)
	}
	if got := len(srv.Requests("thread/turns/list")); got != reads {
		t.Fatalf("thread/turns/list read %d times after the restart, want no read of a thread no turn ended on", got-reads)
	}
}

// A bridge whose attachment is lost while the terminal goes on is replaced.
func TestStandardCodexReconnectsALostBridge(t *testing.T) {
	t.Parallel()
	e, rt, _ := newCodexEnv(t)
	run, _ := e.launchOn(t, "codex")
	srv, bridge := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(run.ID) })
	_ = bridge.agentOut.Close()
	rt.bridge(t, 2)
	dialed(t, srv, 2)
	waitFor(t, "the reconnected client", func() bool { return e.sched.Wakeable(run.ID) })
	if err := e.sched.Wake(t.Context(), run.ID, "mail"); err != nil {
		t.Fatalf("Wake after the reconnect: %v", err)
	}
}

// A connect that fails while the run is paused is retried when it resumes.
func TestStandardCodexConnectsAfterAPause(t *testing.T) {
	t.Parallel()
	e, rt, _ := newCodexEnv(t)
	rt.mu.Lock()
	rt.startErr = errors.New("container is paused")
	rt.mu.Unlock()
	run, _ := e.launchOn(t, "codex")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.startErr = nil
	rt.mu.Unlock()
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	srv, _ := rt.bridge(t, 1)
	dialed(t, srv, 1)
	srv.StartThread("thread-1", false, "")
	waitFor(t, "the connected client", func() bool { return e.sched.Wakeable(run.ID) })
}

func TestStandardCodexWithoutItsAppServerShowsWhy(t *testing.T) {
	t.Parallel()
	e, rt, _ := newCodexEnv(t)
	rt.startErr = errors.New("no such container")
	run, _ := e.launchOn(t, "codex")
	parked := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !strings.Contains(parked.Reason, "codex app-server unavailable: start the bridge: no such container") {
		t.Fatalf("reason = %q", parked.Reason)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "hello"}, false, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	e.pty.mu.Lock()
	defer e.pty.mu.Unlock()
	if len(e.pty.injects) != 1 || e.pty.injects[0].message != "hello" {
		t.Fatalf("terminal injects = %+v, want the message typed into the terminal", e.pty.injects)
	}
}

func promptTexts(t *testing.T, params []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, raw := range params {
		var p struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || len(p.Input) != 1 {
			t.Fatalf("params = %s", raw)
		}
		out = append(out, p.Input[0].Text)
	}
	return out
}
