package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// guestRuntime stands in for the VM runtime's guests. A VM whose disks are
// held has its vCPUs stopped, and one made silent has a guest that stopped
// answering. A call that needs the guest's answer then waits for it until
// its context ends, as the helper's client does; an exec in a VM with its
// vCPUs stopped is refused at once, as the engine refuses it.
type guestRuntime struct {
	*diskRuntime
	guestMu sync.Mutex
	silent  map[runtime.ID]chan struct{}
	halted  map[runtime.ID]bool
}

func (r *guestRuntime) silence(id runtime.ID) {
	r.guestMu.Lock()
	defer r.guestMu.Unlock()
	if r.silent[id] == nil {
		r.silent[id] = make(chan struct{})
	}
}

func (r *guestRuntime) answer(id runtime.ID) {
	r.guestMu.Lock()
	defer r.guestMu.Unlock()
	if ch := r.silent[id]; ch != nil {
		close(ch)
		delete(r.silent, id)
	}
	delete(r.halted, id)
}

func (r *guestRuntime) answers(ctx context.Context, id runtime.ID) error {
	r.guestMu.Lock()
	ch := r.silent[id]
	r.guestMu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("guest: %s: %w", id, ctx.Err())
	}
}

func (r *guestRuntime) isHalted(id runtime.ID) bool {
	r.guestMu.Lock()
	defer r.guestMu.Unlock()
	return r.halted[id]
}

func (r *guestRuntime) refuseHalted(id runtime.ID) error {
	if r.isHalted(id) {
		return fmt.Errorf("vm runtime: VM %s: it is paused", id)
	}
	return nil
}

func (r *guestRuntime) HoldDisks(ctx context.Context, id runtime.ID) error {
	r.silence(id)
	r.guestMu.Lock()
	r.halted[id] = true
	r.guestMu.Unlock()
	return r.diskRuntime.HoldDisks(ctx, id)
}

func (r *guestRuntime) Resume(ctx context.Context, id runtime.ID) error {
	if err := r.diskRuntime.Resume(ctx, id); err != nil {
		return err
	}
	r.answer(id)
	return nil
}

// Stop ends the VM: a call into its guest fails at once from then on.
func (r *guestRuntime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	r.answer(id)
	return r.diskRuntime.Stop(ctx, id, grace)
}

func (r *guestRuntime) Attach(ctx context.Context, id runtime.ID) (runtime.Attachment, error) {
	att, err := r.diskRuntime.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	return guestAttachment{Attachment: att, r: r, id: id}, nil
}

type guestAttachment struct {
	runtime.Attachment
	r  *guestRuntime
	id runtime.ID
}

func (a guestAttachment) Resize(ctx context.Context, cols, rows uint) error {
	if err := a.r.answers(ctx, a.id); err != nil {
		return err
	}
	return a.Attachment.Resize(ctx, cols, rows)
}

func (r *guestRuntime) Exec(ctx context.Context, id runtime.ID, argv []string, workDir string) (int, string, string, error) {
	if err := r.refuseHalted(id); err != nil {
		return 0, "", "", err
	}
	if err := r.answers(ctx, id); err != nil {
		return 0, "", "", err
	}
	return r.diskRuntime.Exec(ctx, id, argv, workDir)
}

func (r *guestRuntime) StartExecPipe(ctx context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	if err := r.refuseHalted(id); err != nil {
		return nil, err
	}
	if err := r.answers(ctx, id); err != nil {
		return nil, err
	}
	return r.diskRuntime.StartExecPipe(ctx, id, spec)
}

func (r *guestRuntime) RecoverExec(ctx context.Context, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	if err := r.answers(ctx, identity.ContainerID); err != nil {
		return nil, err
	}
	return r.diskRuntime.RecoverExec(ctx, identity)
}

// guestEnv is an Enhanced environment, which also launches Background
// sessions of enhancedAgent, on guestRuntime with the real PTY host, which
// makes the calls into the guest that a session's terminal needs. free is
// the data filesystem's free space, of 100 GiB with a 10 GiB reserve.
type guestEnv struct {
	*testEnv
	guests      *guestRuntime
	free        atomic.Uint64
	transcripts string
	host        *ptyhost.Host
}

func newGuestEnv(t *testing.T) *guestEnv {
	t.Helper()
	g := &guestEnv{transcripts: t.TempDir()}
	g.free.Store(50 << 30)
	g.testEnv, _ = newBackgroundEnv(t, enhancedAgent, true, func(cfg *Config) {
		g.guests = &guestRuntime{
			diskRuntime: &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}, held: map[runtime.ID]bool{}},
			silent:      map[runtime.ID]chan struct{}{},
			halted:      map[runtime.ID]bool{},
		}
		cfg.Runtime = g.guests
		cfg.PTY = g.newHost(t)
		cfg.MinFreeBytes = 10 << 30
		cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: g.free.Load()}, nil
		}
	})
	return g
}

func (g *guestEnv) newHost(t *testing.T) *ptyhost.Host {
	t.Helper()
	host, err := ptyhost.New(ptyhost.Config{TranscriptDir: g.transcripts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	g.host = host
	return host
}

// restart stops the server process and starts the next one on the same
// state, runtime and transcripts, and requires its recovery to finish.
func (g *guestEnv) restart(t *testing.T, within time.Duration) *Scheduler {
	t.Helper()
	if err := g.sched.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.host.Close(); err != nil {
		t.Fatal(err)
	}
	// The next process's git engine watches nothing yet.
	g.git.mu.Lock()
	clear(g.git.watching)
	g.git.mu.Unlock()
	cfg := g.cfg
	cfg.PTY = g.newHost(t)
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	startScheduler(t, next)
	select {
	case <-next.RecoveryReady():
	case <-time.After(within):
		t.Fatalf("recovery did not finish within %s", within)
	}
	g.sched = next
	return next
}

// holdForDisk has the disk guard pause runs and stop their VMs: the data
// filesystem is below its reserve and their disks grew.
func (g *guestEnv) holdForDisk(t *testing.T, runs ...domain.RunID) {
	t.Helper()
	g.free.Store(5 << 30)
	g.sched.guardVMDisks(t.Context())
	g.guests.mu.Lock()
	for _, run := range runs {
		g.guests.use[g.cid(t, run)] += 1 << 30
	}
	g.guests.mu.Unlock()
	g.sched.guardVMDisks(t.Context())
	for _, run := range runs {
		if !g.sched.Paused(run) || !g.guests.isHalted(g.cid(t, run)) {
			t.Fatalf("the disk guard did not pause session %s and stop its VM", run)
		}
	}
}

func (g *guestEnv) requireTerminalOutput(t *testing.T, run domain.RunID) {
	t.Helper()
	c, err := g.rt.get(g.cid(t, run))
	if err != nil {
		t.Fatal(err)
	}
	c.output("back\r\n")
	waitFor(t, "the terminal's output", func() bool {
		line, err := g.host.LastLine(t.Context(), run, 0)
		return err == nil && line == "back"
	})
}

// requireBack requires everything the member of an Enhanced session uses to
// work: the terminal, the agent's conversation with the turn it held before,
// and the diff watch.
func (g *guestEnv) requireBack(t *testing.T, run domain.RunID) {
	t.Helper()
	g.requireTerminalOutput(t, run)
	if _, err := g.sched.Inject(t.Context(), run, g.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatalf("message to the agent of session %s: %v", run, err)
	}
	waitItems(t, g.sched, run, "the task's turn and the new one", turnEnded("end_turn", 2))
	if _, ok := g.git.watchingFor(run); !ok {
		t.Errorf("session %s has no diff watch", run)
	}
}

// The disk guard pauses a session and stops its VM, whose guest then answers
// nothing, and a second hold soon after leaves the session for its member.
// A server that restarts meanwhile takes every such session up as it was
// without waiting for its guest, so recovery finishes and the guards start.
// Each session can be killed, and the guard's resume or its member's brings
// it back whole.
func TestARestartTakesUpSessionsWhoseVMsTheDiskGuardStopped(t *testing.T) {
	t.Parallel()
	g := newGuestEnv(t)
	var mine, guards, killed domain.RunID
	for _, run := range []*domain.RunID{&mine, &guards, &killed} {
		*run = g.launchACP(t, "say pong").ID
		waitItems(t, g.sched, *run, "the task's turn", turnEnded("end_turn", 1))
		g.guests.mu.Lock()
		g.guests.use[g.cid(t, *run)] = 0
		g.guests.mu.Unlock()
	}
	sub := g.subscribe(t)
	g.holdForDisk(t, mine)
	g.free.Store(50 << 30)
	g.sched.guardVMDisks(t.Context())
	if g.sched.Paused(mine) {
		t.Fatal("the disk guard did not resume the session once there was space")
	}
	g.holdForDisk(t, mine, guards, killed)
	waitFor(t, "the note that leaves the session to its member", func() bool {
		select {
		case ev := <-sub.Events():
			p, ok := ev.Payload.(events.TimelinePayload)
			return ok && ev.RunID == mine && p.Message == diskReheldNote
		default:
			return false
		}
	})

	next := g.restart(t, waitTimeout)
	for _, run := range []domain.RunID{mine, guards, killed} {
		if !next.Paused(run) || !g.guests.isHalted(g.cid(t, run)) {
			t.Fatalf("after the restart session %s is not paused with its VM stopped", run)
		}
	}
	if err := next.Kill(t.Context(), killed, g.member.ID); err != nil {
		t.Fatalf("kill a session whose VM is stopped: %v", err)
	}
	g.waitStoreStatus(t, killed, domain.RunAbandoned)

	g.free.Store(50 << 30)
	next.guardVMDisks(t.Context())
	if next.Paused(guards) || !next.Paused(mine) {
		t.Fatalf("with space back the guard's own hold is paused %v and the one it left to its member %v; want false and true", next.Paused(guards), next.Paused(mine))
	}
	g.requireBack(t, guards)
	if err := next.Resume(t.Context(), mine, g.member.ID); err != nil {
		t.Fatal(err)
	}
	g.requireBack(t, mine)
}

// A guest that stopped answering while its VM runs holds up a restart only
// for the bound of each step that waits on it. Its session is kept: a
// terminal that could not be taken up says why, and the agent's terminal
// carries on once the guest answers again.
func TestARestartKeepsASessionWhoseGuestDoesNotAnswer(t *testing.T) {
	t.Parallel()
	g := newGuestEnv(t)
	run, c := g.launchFake(t, "work")
	shell := &runTerminalSet{Version: 1, RunID: run.ID, Terminals: map[string]*runTerminal{"shell": {
		ID: "shell", Incarnation: "one", Cols: 80, Rows: 24,
		Identity: runtime.ExecIdentity{ContainerID: c.id, ExecID: "exec", CreationKey: "key", ClaimToken: "claim"},
	}}}
	if err := g.sched.persistRunTerminalsLocked(run.ID, shell); err != nil {
		t.Fatal(err)
	}
	g.guests.silence(c.id)

	next := g.restart(t, recoveryStepTimeout+waitTimeout)
	if r, err := g.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("after the restart the session is %v, %v; want it kept running", r, err)
	}
	if _, err := g.rt.get(c.id); err != nil {
		t.Fatalf("after the restart the session's container: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	terminal, err := next.LookupDevelopmentTerminal(ctx, run.ID, "shell")
	cancel()
	if err != nil || terminal.Process.State != "unavailable" || !strings.Contains(terminal.Process.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("the terminal that could not be taken up is %+v, %v; want it unavailable with the guest's error", terminal.Process, err)
	}
	g.guests.answer(c.id)
	terminal, err = next.LookupDevelopmentTerminal(t.Context(), run.ID, "shell")
	if err != nil || strings.Contains(terminal.Process.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("the terminal once the guest answers is %+v, %v; want the request to have asked the guest again", terminal.Process, err)
	}
	g.requireTerminalOutput(t, run.ID)
	if err := next.Pause(t.Context(), run.ID, g.member.ID); err != nil {
		t.Fatalf("pause the kept session: %v", err)
	}
}

// An Enhanced agent the restarted server cannot reach in time is kept, with
// the error in its conversation, and a Background session is not ended for
// it. Once the guest answers, pausing and resuming the session, as Retry
// Enhanced does, attaches to the same agent and its turn.
func TestARestartKeepsAnEnhancedSessionWhoseGuestDoesNotAnswer(t *testing.T) {
	t.Parallel()
	g := newGuestEnv(t)
	interactive := g.launchACP(t, "say pong").ID
	waitItems(t, g.sched, interactive, "the task's turn", turnEnded("end_turn", 1))
	background := g.launchBackground(t, enhancedAgent, acpmock.PromptWait).ID
	waitFor(t, "the Background session's turn", func() bool {
		sess := g.sched.acp.session(background)
		return sess != nil && sess.State().TurnInFlight
	})
	agents := len(g.guests.all())
	for _, run := range []domain.RunID{interactive, background} {
		g.guests.silence(g.cid(t, run))
	}

	next := g.restart(t, acpConnectTimeout+waitTimeout)
	if r := g.waitStoreStatus(t, interactive, domain.RunNeedsAttention); !strings.Contains(r.Reason, acpFailedReason) || !strings.Contains(r.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("the session whose agent was not reached reads %q; want it disconnected with the guest's error", r.Reason)
	}
	for _, run := range []domain.RunID{interactive, background} {
		waitItems(t, next, run, "the notice of the agent that was not reached", func(items []acphost.Item) bool {
			return slices.ContainsFunc(items, func(it acphost.Item) bool {
				return it.Kind == acphost.KindNotice && it.Notice.Title == "Enhanced agent failed" &&
					strings.Contains(it.Notice.Description, context.DeadlineExceeded.Error())
			})
		})
		g.guests.answer(g.cid(t, run))
		if err := next.Pause(t.Context(), run, g.member.ID); err != nil {
			t.Fatalf("pause session %s to retry its agent: %v", run, err)
		}
		if err := next.Resume(t.Context(), run, g.member.ID); err != nil {
			t.Fatalf("resume session %s to retry its agent: %v", run, err)
		}
		if next.acp.session(run) == nil {
			t.Fatalf("session %s has no agent after the retry: %v", run, next.acp.openError(run))
		}
	}
	if got := len(g.guests.all()); got != agents {
		t.Fatalf("%d agents after the retries, want the %d from before the restart", got, agents)
	}
	g.requireBack(t, interactive)
	if !next.acp.session(background).State().TurnInFlight {
		t.Fatal("the Background session's turn did not carry on")
	}
	if err := next.ACPCancel(t.Context(), background); err != nil {
		t.Fatal(err)
	}
	if r := g.waitStoreStatus(t, background, domain.RunFailed); !strings.Contains(r.Reason, "cancelled") {
		t.Fatalf("the Background session ended with %q; want its cancelled turn", r.Reason)
	}
}
