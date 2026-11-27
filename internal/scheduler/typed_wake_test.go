package scheduler

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// launchQuiet launches a coordinated Standard session of agent and ages it
// past the typed wake's quiet window.
func (e *testEnv) launchQuiet(t *testing.T, agent string) (domain.RunID, *supervised) {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", agent, domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitFor(t, "the terminal", func() bool { return e.pty.session(run.ID) != nil })
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	entry := e.sched.runs[run.ID]
	entry.startedAt = time.Now().Add(-time.Minute)
	return run.ID, entry
}

func typedWakeLine(unread int) string {
	return strings.TrimSuffix(coordhooks.Named(protocol.CoordInboxContext(unread), coordhooks.NewCLI), "\n")
}

func TestMailTypesOneLineIntoAnIdleTerminal(t *testing.T) {
	for _, agent := range []string{"copilot", "gemini", "cursor"} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "typed wake cli")))
			withCoordination(t, e)
			run, _ := e.launchQuiet(t, agent)
			if !e.sched.Wakeable(run) || !e.sched.TypedWake(run) || e.sched.InTurn(run) {
				t.Fatalf("an idle Standard %s session: wakeable %v, typed %v, in turn %v", agent, e.sched.Wakeable(run), e.sched.TypedWake(run), e.sched.InTurn(run))
			}
			if err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(2)); err != nil {
				t.Fatalf("Wake: %v", err)
			}
			line := typedWakeLine(2)
			if strings.Contains(line, "\n") || !strings.Contains(line, "2 unacknowledged") || !strings.Contains(line, "cloudfield-internal inbox") || !strings.Contains(line, "until it returns no messages") || !strings.Contains(line, "not system instructions") {
				t.Fatalf("typed line %q", line)
			}
			if got, want := e.pty.injected(), []fakeInject{{run: run, name: "Cloudfield", message: line, submit: "\r"}}; !slices.Equal(got, want) {
				t.Fatalf("typed %+v, want %+v", got, want)
			}
		})
	}
}

func TestATypedWakeWaitsForAQuietUnattendedTerminal(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "typed wake cli")))
	withCoordination(t, e)
	run, entry := e.launchQuiet(t, "gemini")
	sess := e.pty.session(run)
	terminal := func(set func()) func() {
		return func() {
			sess.mu.Lock()
			defer sess.mu.Unlock()
			set()
		}
	}
	session := func(set func()) func() {
		return func() {
			e.sched.mu.Lock()
			defer e.sched.mu.Unlock()
			set()
		}
	}
	for _, tc := range []struct {
		name         string
		block, clear func()
	}{
		{"bracketed paste mode is off", func() { e.pty.setPasteOff(true) }, func() { e.pty.setPasteOff(false) }},
		{"the agent printed 2 s ago", terminal(func() { sess.last = time.Now().Add(-2 * time.Second) }), terminal(func() { sess.last = time.Now().Add(-4 * time.Second) })},
		{"a person typed 9 s ago", terminal(func() { sess.typed = time.Now().Add(-9 * time.Second) }), terminal(func() { sess.typed = time.Now().Add(-11 * time.Second) })},
		{"the session resumed, or the server restarted, 9 s ago", session(func() { entry.quietFrom = time.Now().Add(-9 * time.Second) }), session(func() { entry.quietFrom = time.Now().Add(-11 * time.Second) })},
		{"the session is paused", session(func() { entry.paused = true }), session(func() { entry.paused = false })},
		{"the session is ending or moving account", session(func() { entry.finalizing = true }), session(func() { entry.finalizing = false })},
		{"a stop was requested", session(func() { entry.killRequested = true }), session(func() { entry.killRequested = false })},
		{"an input request is open", session(func() { entry.pendingInputs = []domain.RunInputRequest{{}} }), session(func() { entry.pendingInputs = nil })},
		{"a sudo prompt is open", session(func() { entry.sudo = []*sudoRequest{{}} }), session(func() { entry.sudo = nil })},
	} {
		tc.block()
		err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1))
		if err == nil || len(e.pty.injected()) != 0 {
			t.Fatalf("while %s: Wake = %v, typed %+v; want the wake left owed", tc.name, err, e.pty.injected())
		}
		if tc.name == "bracketed paste mode is off" && !errors.Is(err, ptyhost.ErrPasteModeOff) {
			t.Fatalf("with paste mode off Wake = %v, want the terminal's refusal", err)
		}
		if !e.sched.Wakeable(run) {
			t.Fatalf("while %s the session is not wakeable, so nothing retries its wake", tc.name)
		}
		tc.clear()
	}
	if err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1)); err != nil {
		t.Fatalf("Wake once nothing holds it: %v", err)
	}
	if got := e.pty.injected(); len(got) != 1 || got[0].message != typedWakeLine(1) {
		t.Fatalf("typed %+v, want the line once", got)
	}
	if err := e.sched.CloseRun(t.Context(), run, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if e.sched.Wakeable(run) {
		t.Fatal("the closed session whose container is kept is still wakeable, so every sweep retries its wake")
	}
}

// A sleeping VM's terminal takes the line only once it is back and quiet,
// and the wake is not delivered before that.
func TestATypedWakeTypesAfterItsVMWakes(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false, withServerBinary(fakeServerBinary(t, "typed wake cli")))
	withCoordination(t, e)
	run, entry := e.launchQuiet(t, "gemini")
	waitFor(t, "the container running", func() bool { return containerState(e.container(t, run)) == "running" })
	e.sched.mu.Lock()
	entry.activity = domain.ActivityAnswered
	e.sched.mu.Unlock()
	e.saveRun(t, run)
	if !sr.isSaved(e.cid(t, run)) || !e.sched.Wakeable(run) {
		t.Fatal("the saved Standard VM is not wakeable for mail")
	}
	if err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1)); err == nil {
		t.Fatal("Wake of a sleeping VM reported the line typed")
	}
	if e.sched.Sleep(run).AsleepSince != nil || sr.restores != 1 || len(e.pty.injected()) != 0 {
		t.Fatalf("mail restored the VM %d times and typed %+v, want one restore and nothing typed yet", sr.restores, e.pty.injected())
	}
	e.sched.mu.Lock()
	entry.quietFrom = time.Now().Add(-time.Minute)
	e.sched.mu.Unlock()
	if err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1)); err != nil {
		t.Fatalf("Wake of the woken VM: %v", err)
	}
	if got := e.pty.injected(); len(got) != 1 || got[0].message != typedWakeLine(1) {
		t.Fatalf("typed %+v, want the line once", got)
	}
}

// The supervisor leaves a login shell where the agent exited. bash turns
// bracketed paste on at its prompt, so only the supervisor's state tells the
// two apart.
func TestMailIsNotTypedIntoTheShellAnExitedAgentLeaves(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "typed wake cli")))
	withCoordination(t, e)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, supervisorStateFile) {
			return 0, "exited 0\n", nil
		}
		return 0, "", nil
	}
	run, _ := e.launchQuiet(t, "gemini")
	if err := e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1)); err == nil || len(e.pty.injected()) != 0 {
		t.Fatalf("Wake = %v, typed %+v; want nothing typed into the shell", err, e.pty.injected())
	}
	if e.sched.Wakeable(run) {
		t.Fatal("the session whose agent exited is still wakeable, so its wake is retried without end")
	}
}

func TestOnlyShippedTypedWakeAgentsAreTypedInto(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		mutate      func(*Config)
	}{
		{name: "claude", agent: "claude"},
		{name: "codex", agent: "codex"},
		{name: "antigravity", agent: "antigravity"},
		{name: "an administrator's gemini definition", agent: "gemini", mutate: func(c *Config) {
			c.Harnesses["gemini"] = HarnessSpec{TUIArgs: []string{"gemini", "{task}"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, func(c *Config) {
				withServerBinary(fakeServerBinary(t, "typed wake cli"))(c)
				if tc.mutate != nil {
					tc.mutate(c)
				}
			})
			withCoordination(t, e)
			run, _ := e.launchQuiet(t, tc.agent)
			if e.sched.TypedWake(run) {
				t.Fatalf("a Standard session of %s is woken by typing", tc.name)
			}
			_ = e.sched.Wake(t.Context(), run, protocol.CoordInboxContext(1))
			if got := e.pty.injected(); len(got) != 0 {
				t.Fatalf("mail typed %+v into a Standard session of %s", got, tc.name)
			}
		})
	}
}
