package scheduler

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

const sessionLimit = "You've hit your session limit · resets 3pm (UTC)"

func standardLimit(t *testing.T) agentstatus.Report {
	t.Helper()
	limit, ok := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"` + sessionLimit + `"}`))
	if !ok {
		t.Fatal("the StopFailure hook was not mapped")
	}
	return limit
}

func (e *loginShareEnv) report(t *testing.T, run domain.RunID, report agentstatus.Report) {
	t.Helper()
	if err := e.sched.ReportAgentState(t.Context(), run, report); err != nil {
		t.Fatal(err)
	}
}

func (e *loginShareEnv) publishLimit(t *testing.T, run domain.RunID) {
	t.Helper()
	if _, err := e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run, Payload: events.RunUsageLimitPayload{
		Message: sessionLimit, SessionID: "sess-1",
	}}); err != nil {
		t.Fatal(err)
	}
}

// launchOwn launches Ada's session in mode on her default login, past its
// start, with its conversation reported.
func (e *loginShareEnv) launchOwn(t *testing.T, mode domain.LaunchMode) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", mode)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if mode == domain.LaunchTUI {
		e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
	}
	return run
}

// assertMovedOnceTo waits for the live Standard or headless run to be on
// login, moved once, in one container that mounts login and resumes the
// conversation with the continuation alone, never the task again.
func (e *loginShareEnv) assertMovedOnceTo(t *testing.T, run domain.RunID, login domain.Account, notes *roomNotes, continuation string) {
	t.Helper()
	waitFor(t, "the session on "+login.String(), func() bool {
		row, err := e.db.GetRun(t.Context(), run)
		c := e.rt.byName(string(run))
		return err == nil && row.Account() == login && !row.Status.Terminal() && c != nil && e.mountsLogin(t, c.spec, login)
	})
	segments, err := e.db.ListRunAccountSegments(t.Context(), run)
	if err != nil || len(segments) != 2 || segments[1].Account() != login {
		t.Fatalf("segments = %+v, %v; want one move, to %s", segments, err, login)
	}
	c := e.rt.byName(string(run))
	if e.containerCount() != 1 {
		t.Fatalf("%d containers; want only the session's one", e.containerCount())
	}
	if !slices.Contains(c.spec.Command, continuation) || slices.ContainsFunc(c.spec.Command, func(arg string) bool { return strings.Contains(arg, "fix it") }) {
		t.Fatalf("replacement command %q; want the continuation and not the task", c.spec.Command)
	}
	waitFor(t, "one room note", func() bool {
		return slices.Equal(notes.all(), []string{"Continued on Ada (second)'s account because Ada's usage ran out."})
	})
	waitFor(t, "the limit acted on", func() bool {
		sc, scErr := e.sched.readSidecar(run)
		return scErr == nil && sc.UsageLimit == "" && sc.AccountSwap == nil
	})
}

// The usage limit and the end of the turn it ended race. A headless
// session's adapter reads the limit before or after the agent exits, and a
// Standard session's hook reports it before or after an idle report of the
// same turn; either way the session moves to Ada's second login once.
func TestALoginMoveRacesTheEndOfTheTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"headless: limit, then exit", "headless: exit, then limit", "standard: limit, then idle", "standard: idle, then limit"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e, _, notes, _, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			mode := domain.LaunchTUI
			if strings.HasPrefix(tc, "headless") {
				mode = domain.LaunchHeadless
			}
			run := e.launchOwn(t, mode)
			idle := agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}
			switch tc {
			case "headless: limit, then exit":
				e.publishLimit(t, run.ID)
				waitFor(t, "the limit recorded", func() bool {
					sc, err := e.sched.readSidecar(run.ID)
					return err == nil && sc.UsageLimit != ""
				})
				e.rt.byName(string(run.ID)).exitNow(1)
			case "headless: exit, then limit":
				e.rt.byName(string(run.ID)).exitNow(1)
				waitFor(t, "the exit observed", func() bool {
					e.sched.mu.Lock()
					defer e.sched.mu.Unlock()
					entry := e.sched.runs[run.ID]
					return entry != nil && entry.exitObserved
				})
				e.publishLimit(t, run.ID)
			case "standard: limit, then idle":
				e.report(t, run.ID, standardLimit(t))
				e.report(t, run.ID, idle)
			case "standard: idle, then limit":
				e.report(t, run.ID, idle)
				e.report(t, run.ID, standardLimit(t))
			}
			e.assertMovedOnceTo(t, run.ID, second, notes, continuedOnSecond)
		})
	}
}

// A second usage-limit signal while a move to Ada's second login holds the
// session, a repeat from the agent it replaces, starts no second move.
func TestASecondLimitDuringALoginMoveIsTheSameMove(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e, _, notes, _, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			run := e.launchOwn(t, mode)
			old := e.rt.byName(string(run.ID)).id
			var repeated atomic.Bool
			e.rt.mu.Lock()
			e.rt.destroyHook = func(id runtime.ID) {
				if id != old || repeated.Swap(true) {
					return
				}
				if mode == domain.LaunchTUI {
					e.report(t, run.ID, standardLimit(t))
					return
				}
				e.publishLimit(t, run.ID)
				time.Sleep(100 * time.Millisecond)
			}
			e.rt.mu.Unlock()
			if mode == domain.LaunchTUI {
				e.report(t, run.ID, standardLimit(t))
			} else {
				e.publishLimit(t, run.ID)
				waitFor(t, "the limit recorded", func() bool {
					sc, err := e.sched.readSidecar(run.ID)
					return err == nil && sc.UsageLimit != ""
				})
				e.rt.byName(string(run.ID)).exitNow(1)
			}
			e.assertMovedOnceTo(t, run.ID, second, notes, continuedOnSecond)
			if !repeated.Load() {
				t.Fatal("the repeat was never sent")
			}
		})
	}
}

// moveGap runs gap once, under the authorization mutex as an RPC would,
// while a move of run holds its chosen login and has not recorded it: when
// the old container is removed.
func (e *loginShareEnv) moveGap(run domain.RunID, mu *sync.Mutex, gap func()) *atomic.Bool {
	old := e.rt.byName(string(run)).id
	var ran atomic.Bool
	e.rt.mu.Lock()
	e.rt.destroyHook = func(id runtime.ID) {
		if id != old || ran.Swap(true) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		gap()
	}
	e.rt.mu.Unlock()
	return &ran
}

// Removing Ada's second login while a move has chosen it and not recorded
// it is refused: the move's marker lists the session, and the move then
// lands on the login it chose.
func TestRemovingALoginAMoveChoseIsRefused(t *testing.T) {
	t.Parallel()
	e, _, notes, usage, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	mu := &sync.Mutex{}
	e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: mu, Note: notes.post})
	startScheduler(t, e.sched)
	<-e.sched.RecoveryReady()
	run := e.launchOwn(t, domain.LaunchTUI)
	var inUse map[domain.RunID]domain.MemberID
	var inUseErr error
	ran := e.moveGap(run.ID, mu, func() { inUse, inUseErr = e.sched.LoginInUse(t.Context(), second) })
	e.report(t, run.ID, standardLimit(t))
	e.assertMovedOnceTo(t, run.ID, second, notes, continuedOnSecond)
	if !ran.Load() || inUseErr != nil || inUse[run.ID] != e.member.ID {
		t.Fatalf("in the gap LoginInUse = %v, %v; want the moving session, which refuses the removal", inUse, inUseErr)
	}
}

// What changes on the chosen login between choosing and recording a move:
// automatic turned off governs the next choice, so the move in flight
// lands on it; turned off for shares, a teammate's move fails as a revoke
// in that gap does, and nothing mounts the login.
func TestTurningAChosenLoginOffDuringAMove(t *testing.T) {
	t.Parallel()
	t.Run("automatic off", func(t *testing.T) {
		t.Parallel()
		e, _, notes, usage, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
		mu := &sync.Mutex{}
		e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: mu, Note: notes.post})
		startScheduler(t, e.sched)
		<-e.sched.RecoveryReady()
		run := e.launchOwn(t, domain.LaunchTUI)
		ran := e.moveGap(run.ID, mu, func() {
			if err := e.db.SetAgentLoginAutomatic(t.Context(), e.member.ID, "claude", second.Login, false); err != nil {
				panic(err)
			}
		})
		e.report(t, run.ID, standardLimit(t))
		e.assertMovedOnceTo(t, run.ID, second, notes, continuedOnSecond)
		if !ran.Load() {
			t.Fatal("the gap never ran")
		}
	})
	t.Run("automatic off on a teammate's login", func(t *testing.T) {
		t.Parallel()
		e, _, notes, usage, _ := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
		mu := &sync.Mutex{}
		e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: mu, Note: notes.post})
		e.share(t)
		work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
		spare := e.addLogin(t, e.owner.ID, "lgn-spare00000", "spare", workLogin)
		if err := e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
			t.Fatal(err)
		}
		usage.set(work, 100)
		startScheduler(t, e.sched)
		<-e.sched.RecoveryReady()
		run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "fix it", "claude", domain.LaunchTUI, domain.LaunchOptions{AccountLogin: work.Login})
		if err != nil {
			t.Fatal(err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
		ran := e.moveGap(run.ID, mu, func() {
			if err := e.db.SetAgentLoginAutomatic(t.Context(), e.owner.ID, "claude", spare.Login, false); err != nil {
				panic(err)
			}
		})
		e.report(t, run.ID, standardLimit(t))
		waitFor(t, "the session on spare", func() bool {
			row, err := e.db.GetRun(t.Context(), run.ID)
			if err == nil && row.Status.Terminal() {
				t.Fatalf("run %s (%q); want it moved to spare", row.Status, row.Reason)
			}
			c := e.rt.byName(string(run.ID))
			return err == nil && row.Account() == spare && c != nil && e.mountsLogin(t, c.spec, spare)
		})
		if !ran.Load() {
			t.Fatal("the gap never ran")
		}
	})
	t.Run("off for shares", func(t *testing.T) {
		t.Parallel()
		e, _, notes, usage, _ := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
		mu := &sync.Mutex{}
		e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: mu, Note: notes.post})
		e.share(t)
		work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
		spare := e.addLogin(t, e.owner.ID, "lgn-spare00000", "spare", workLogin)
		if err := e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
			t.Fatal(err)
		}
		usage.set(work, 100)
		startScheduler(t, e.sched)
		<-e.sched.RecoveryReady()
		run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "fix it", "claude", domain.LaunchTUI, domain.LaunchOptions{AccountLogin: work.Login})
		if err != nil {
			t.Fatal(err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
		ran := e.moveGap(run.ID, mu, func() {
			if err := e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", spare.Login, false); err != nil {
				panic(err)
			}
			e.sched.RevokeLogin(spare)
		})
		e.report(t, run.ID, standardLimit(t))
		got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
		want := "Usage limit reached on Grace (work)'s account; continuing on Grace (spare)'s account failed: "
		if !ran.Load() || !strings.HasPrefix(got.Reason, want) || !strings.Contains(got.Reason, "not shared with you") || got.Account() != work {
			t.Fatalf("run on %s failed with %q; want it left on work with %q and the refusal", got.Account(), got.Reason, want)
		}
		if segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID); err != nil || len(segments) != 0 || e.containerCount() != 0 || len(notes.all()) != 0 {
			t.Fatalf("segments %+v, %v, %d containers, notes %v; want no move recorded and nothing on spare", segments, err, e.containerCount(), notes.all())
		}
	})
}

// A server can stop at any step of a move between Ada's two logins. Each
// restart leaves the run on the login its container mounts, never sends
// the user's prompt again, sends the continuation at most once, and tells
// Ada's default login from her second though both are hers.
func TestRestartAtEachStepOfALoginMove(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"marker written", "old container removed", "row recorded, old container", "new container started", "continuation being sent"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			const asked = "keep going"
			if _, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: asked}, false, nil); err != nil {
				t.Fatal(err)
			}
			moved := step == "new container started" || step == "continuation being sent"
			if moved {
				_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
				waitFor(t, "the move settled", func() bool {
					row, rowErr := e.db.GetRun(t.Context(), run.ID)
					sc, scErr := e.sched.readSidecar(run.ID)
					_, liveErr := e.sched.acp.live(t.Context(), run.ID)
					return rowErr == nil && scErr == nil && liveErr == nil && row.Account() == second && sc.AccountSwap == nil
				})
			}
			if err = e.sched.Close(); err != nil {
				t.Fatal(err)
			}
			sc, err := e.sched.readSidecar(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			swap := &accountSwap{From: e.member.ID, To: e.member.ID, ToLogin: second.Login, Continuation: continuedOnSecond}
			switch step {
			case "marker written":
				sc.UsageLimit = sessionLimit
			case "old container removed":
				sc.ContainerID = ""
			case "row recorded, old container":
				if _, err = e.db.SetRunAccount(t.Context(), run.ID, second, usageLimitSegment, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "new container started":
				swap.Container = sc.ContainerID
			case "continuation being sent":
				swap.Container, swap.Sending = sc.ContainerID, true
			}
			sc.AccountSwap = swap
			if err = e.sched.writeSidecar(sc); err != nil {
				t.Fatal(err)
			}
			before := len(rt.all())
			cfg := e.cfg
			cfg.PTY = newFakePTY()
			cfg.PTY.(*fakePTY).logDir = e.pty.logDir
			s2, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s2.Close() })
			s2.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			startScheduler(t, s2)

			sent := func() (continuations, prompts int) {
				for _, exec := range rt.all()[before:] {
					for _, text := range sentPrompts(t, exec) {
						switch text {
						case continuedOnSecond:
							continuations++
						case asked:
							prompts++
						}
					}
				}
				return continuations, prompts
			}
			switch step {
			case "old container removed", "row recorded, old container":
				e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
				if e.containerCount() != 0 || len(rt.all()) != before {
					t.Fatalf("%d containers and %d new agents after the restart; want none", e.containerCount(), len(rt.all())-before)
				}
				return
			case "marker written":
				waitFor(t, "the owed move on the second login", func() bool {
					row, rowErr := e.db.GetRun(t.Context(), run.ID)
					continuations, _ := sent()
					return rowErr == nil && row.Account() == second && continuations == 1
				})
			case "new container started":
				waitFor(t, "the owed continuation", func() bool {
					continuations, _ := sent()
					return continuations == 1
				})
			case "continuation being sent":
				waitFor(t, "the notice", func() bool {
					log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
					return readErr == nil && strings.Contains(string(log), "The session moved to Ada (second); the continuation prompt may not have been delivered.")
				})
			}
			waitFor(t, "the marker cleared", func() bool {
				sc, scErr := s2.readSidecar(run.ID)
				return scErr == nil && sc.AccountSwap == nil
			})
			time.Sleep(200 * time.Millisecond)
			continuations, prompts := sent()
			want := 1
			if step == "continuation being sent" {
				want = 0
			}
			row, err := e.db.GetRun(t.Context(), run.ID)
			c := e.rt.byName(string(run.ID))
			switch {
			case err != nil || row.Status.Terminal() || row.Account() != second:
				t.Fatalf("run = %+v, %v; want it live on the second login", row, err)
			case c == nil || e.containerCount() != 1 || !e.mountsLogin(t, c.spec, second):
				t.Fatalf("%d containers; want one, mounting the second login the row records", e.containerCount())
			case continuations != want || prompts != 0:
				t.Fatalf("after the restart %d continuations and %d user prompts; want %d and none", continuations, prompts, want)
			}
		})
	}
}

// A headless session moved to Ada's second login: a usage-limit event the
// replaced container's output still produced arrives once the replacement
// runs. It is dropped, so the replacement's own failure ends the session as
// that failure; the replacement's own limit is followed.
func TestALateLimitFromTheReplacedContainerIsDropped(t *testing.T) {
	t.Parallel()
	for _, from := range []string{"the replaced container", "the replacement"} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()
			e, _, notes, _, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			run := e.launchOwn(t, domain.LaunchHeadless)
			e.publishLimit(t, run.ID)
			waitFor(t, "the limit recorded", func() bool {
				sc, err := e.sched.readSidecar(run.ID)
				return err == nil && sc.UsageLimit != ""
			})
			e.rt.byName(string(run.ID)).exitNow(1)
			e.assertMovedOnceTo(t, run.ID, second, notes, continuedOnSecond)

			late := events.RunUsageLimitPayload{Message: sessionLimit, SessionID: "sess-1", Account: e.member.ID}
			if from == "the replacement" {
				late.Login = second.Login
			}
			if _, err := e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: late}); err != nil {
				t.Fatal(err)
			}
			if from == "the replacement" {
				waitFor(t, "the replacement's limit recorded", func() bool {
					sc, err := e.sched.readSidecar(run.ID)
					return err == nil && sc.UsageLimit != ""
				})
			} else {
				time.Sleep(300 * time.Millisecond)
				if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.UsageLimit != "" {
					t.Fatalf("sidecar limit %q, %v; want the replaced container's dropped", sc.UsageLimit, err)
				}
			}
			e.rt.byName(string(run.ID)).exitNow(1)
			got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
			limited := strings.HasPrefix(got.Reason, usageLimitReason)
			if limited != (from == "the replacement") || got.Account() != second {
				t.Fatalf("run on %s failed with %q; want the usage limit only when the replacement reported it", got.Account(), got.Reason)
			}
		})
	}
}

// hookedLoginUsage runs during before the first usage read, which a move
// makes while the session's old agent still runs.
type hookedLoginUsage struct {
	*loginUsage
	once   sync.Once
	during func()
}

func (u *hookedLoginUsage) ReadLogin(ctx context.Context, account domain.Account, refresh bool) ([]quota.Provider, error) {
	u.once.Do(u.during)
	return u.loginUsage.ReadLogin(ctx, account, refresh)
}

func (u *hookedLoginUsage) Read(ctx context.Context, member domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(member), refresh)
}

// An Enhanced session's agent fails a second turn on its usage limit while
// the move to Ada's second login holds the session: that limit is the
// replaced agent's, and the session moves once, its replacement told once.
func TestARepeatedLimitFromTheReplacedAgentIsDropped(t *testing.T) {
	t.Parallel()
	e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := rt.all()[0]
	e.sched.UseUsageFallback(UsageFallback{Usage: &hookedLoginUsage{loginUsage: usage, during: func() {
		_, _ = e.sched.Inject(context.Background(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
		waitFor(t, "the repeat", func() bool { return len(sentPrompts(t, first)) == 2 })
		time.Sleep(200 * time.Millisecond)
	}}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the session on the second login", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		sc, scErr := e.sched.readSidecar(run.ID)
		_, liveErr := e.sched.acp.live(t.Context(), run.ID)
		return rowErr == nil && scErr == nil && liveErr == nil && row.Account() == second && sc.AccountSwap == nil
	})
	time.Sleep(300 * time.Millisecond)
	execs := rt.all()
	segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID)
	row, rowErr := e.db.GetRun(t.Context(), run.ID)
	switch {
	case err != nil || len(segments) != 2 || len(execs) != 2:
		t.Fatalf("segments %+v, %v, %d agents; want one move", segments, err, len(execs))
	case !slices.Equal(sentPrompts(t, execs[1]), []string{continuedOnSecond}):
		t.Fatalf("the replacement was prompted %q; want the continuation once", sentPrompts(t, execs[1]))
	case rowErr != nil || row.Status.Terminal() || row.Account() != second || len(notes.all()) != 1:
		t.Fatalf("run = %+v, %v, notes %v; want it live on the second login", row, rowErr, notes.all())
	}
}
