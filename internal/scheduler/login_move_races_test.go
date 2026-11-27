package scheduler

import (
	"context"
	"errors"
	"os"
	goruntime "runtime"
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
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
	acp "github.com/coder/acp-go-sdk"
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
func (e *loginShareEnv) assertMovedOnceTo(t *testing.T, run domain.RunID, login domain.Account, notes *roomNotes) {
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
	if !slices.Contains(c.spec.Command, continuedOnSecond) || slices.ContainsFunc(c.spec.Command, func(arg string) bool { return strings.Contains(arg, "fix it") }) {
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
			e.assertMovedOnceTo(t, run.ID, second, notes)
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
			e.assertMovedOnceTo(t, run.ID, second, notes)
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
	e.assertMovedOnceTo(t, run.ID, second, notes)
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
		e.assertMovedOnceTo(t, run.ID, second, notes)
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
				if _, err = e.db.SetRunAccount(t.Context(), run.ID, second, false, usageLimitSegment, time.Now()); err != nil {
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
			e.assertMovedOnceTo(t, run.ID, second, notes)

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

// A member's prompt while the move to Ada's second login holds the session
// is refused rather than sent to the agent the move replaces, which the
// move would stop mid-turn; the session moves once, its replacement told
// once.
func TestAPromptDuringAMoveIsRefused(t *testing.T) {
	t.Parallel()
	e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := rt.all()[0]
	var injectErr error
	e.sched.UseUsageFallback(UsageFallback{Usage: &hookedLoginUsage{loginUsage: usage, during: func() {
		_, injectErr = e.sched.Inject(context.Background(), run.ID, e.member.ID, domain.AgentPrompt{Text: "run a long tool"}, false, nil)
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
	case !errors.Is(injectErr, ErrMoving) || !strings.Contains(injectErr.Error(), "the session is moving to another account or login; send the message when it finishes") ||
		!slices.Equal(sentPrompts(t, first), []string{acpmock.PromptUsageLimit}):
		t.Fatalf("prompt during the move: %v; the replaced agent was prompted %q; want it refused", injectErr, sentPrompts(t, first))
	case err != nil || len(segments) != 2 || len(execs) != 2:
		t.Fatalf("segments %+v, %v, %d agents; want one move", segments, err, len(execs))
	case !slices.Equal(sentPrompts(t, execs[1]), []string{continuedOnSecond}):
		t.Fatalf("the replacement was prompted %q; want the continuation once", sentPrompts(t, execs[1]))
	case rowErr != nil || row.Status.Terminal() || row.Account() != second || len(notes.all()) != 1:
		t.Fatalf("run = %+v, %v, notes %v; want it live on the second login", row, rowErr, notes.all())
	}
}

// owe gives the run's live entry a move's marker whose continuation the
// session is still owed.
func (e *shareEnv) owe(t *testing.T, run domain.RunID, continuation string) (*supervised, *accountSwap) {
	t.Helper()
	swap := &accountSwap{From: e.member.ID, To: e.member.ID, Continuation: continuation}
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	entry := e.sched.runs[run]
	entry.accountSwap = swap
	if err := e.sched.writeSidecar(entry.sidecar()); err != nil {
		t.Fatal(err)
	}
	return entry, swap
}

func (e *shareEnv) marker(entry *supervised) (swap *accountSwap, sending bool) {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if entry.accountSwap != nil {
		sending = entry.accountSwap.Sending
	}
	return entry.accountSwap, sending
}

// A send for one move neither marks nor clears the marker of the move that
// replaced it, and sends nothing.
func TestAContinuationIsSentOnlyForItsOwnMove(t *testing.T) {
	t.Parallel()
	e, rt, _ := newFallbackEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	entry, later := e.owe(t, run.ID, "the later move's continuation")
	e.sched.acp.sendContinuation(entry, &accountSwap{From: e.member.ID, To: e.member.ID, Continuation: "the earlier move's continuation"})
	if got, sending := e.marker(entry); got != later || sending {
		t.Fatalf("marker %+v, sending %v; want the later move's, still owed", got, sending)
	}
	if prompts := sentPrompts(t, rt.all()[0]); len(prompts) != 0 {
		t.Fatalf("prompts %q; want none", prompts)
	}
}

// A continuation whose prompt the closing session refused before any of it
// reached the agent stays owed.
func TestAContinuationTheAgentNeverGotStaysOwed(t *testing.T) {
	t.Parallel()
	e, _, _ := newFallbackEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	entry, swap := e.owe(t, run.ID, "Continue the task where you stopped.")
	sess := e.sched.acp.session(run.ID)
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session gone", func() bool { return e.sched.acp.session(run.ID) == nil })
	// As between finding the session live and sending to it.
	e.sched.acp.mu.Lock()
	e.sched.acp.runs[run.ID].session = sess
	e.sched.acp.mu.Unlock()
	e.sched.acp.sendContinuation(entry, swap)
	sc, err := e.sched.readSidecar(run.ID)
	if got, sending := e.marker(entry); got != swap || sending || err != nil || sc.AccountSwap == nil || sc.AccountSwap.Sending {
		t.Fatalf("marker %+v, sending %v, sidecar %+v, %v; want the continuation still owed", got, sending, sc.AccountSwap, err)
	}
}

// An owed continuation goes to the session once it comes up again in this
// server process; a member's prompt to the conversation first means it is
// not sent late, and the session's log says so.
func TestAnOwedContinuationGoesOnlyToAConversationThatWaited(t *testing.T) {
	t.Parallel()
	const continuation = "Continue the task where you stopped."
	for _, tc := range []string{"the session comes up again", "a member prompts first"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newFallbackEnv(t, enhancedClaude)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			entry, _ := e.owe(t, run.ID, continuation)
			if tc == "a member prompts first" {
				if _, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the notice", func() bool {
					log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
					return readErr == nil && strings.Contains(string(log), "the continuation prompt was not sent because the conversation went on without it")
				})
				time.Sleep(200 * time.Millisecond)
				if got, _ := e.marker(entry); got != nil || !slices.Equal(sentPrompts(t, rt.all()[0]), []string{"say pong"}) {
					t.Fatalf("marker %+v, prompts %q; want the member's prompt alone and the marker dropped", got, sentPrompts(t, rt.all()[0]))
				}
				return
			}
			// Sent to the agent directly, not as a member's prompt.
			_, _ = e.sched.acp.session(run.ID).Prompt(t.Context(), []acp.ContentBlock{acp.TextBlock(acpmock.PromptLoseSession)}, false, nil)
			waitFor(t, "the continuation in the restored session", func() bool {
				execs := rt.all()
				got, _ := e.marker(entry)
				return len(execs) == 2 && slices.Equal(sentPrompts(t, execs[1]), []string{continuation}) && got == nil
			})
		})
	}
}

// The record that a continuation is being sent is made durable, its
// directory entry included, before the send: a state directory that cannot
// be synced leaves the continuation owed and unsent.
func TestAContinuationIsSentOnlyOnceItsRecordIsDurable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root opens a directory whatever its mode")
	}
	e, rt, _ := newFallbackEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	entry, swap := e.owe(t, run.ID, "Continue the task where you stopped.")
	// Files can still be written and renamed in it, but it cannot be opened
	// to be synced.
	if err = os.Chmod(e.cfg.StateDir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.cfg.StateDir, 0o700) })
	e.sched.acp.sendContinuation(entry, swap)
	if err = os.Chmod(e.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, sending := e.marker(entry); got != swap || sending || len(sentPrompts(t, rt.all()[0])) != 0 {
		t.Fatalf("marker %+v, sending %v, prompts %q; want the continuation owed and unsent", got, sending, sentPrompts(t, rt.all()[0]))
	}
}

// Mail that wakes the replacement as its conversation is restored, after a
// move or after a restart that found the continuation owed, waits for the
// continuation: the agent is told of the move first and gets the mail after
// that turn, and no notice says the conversation went on without it.
func TestMailWaitsForTheContinuation(t *testing.T) {
	t.Parallel()
	server := withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n"))
	for _, after := range []string{"move", "restart"} {
		t.Run(after, func(t *testing.T) {
			t.Parallel()
			e, rt, _, _, _ := newLoginMoveEnv(t, func(cfg *Config) {
				enhancedClaude(cfg)
				server(cfg)
			})
			coord, binDir := withCoordination(t, e.testEnv)
			// The first prompt carries the session's instructions.
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			sched, continuation := e.sched, continuedOnSecond
			if after == "restart" {
				if err = e.sched.Close(); err != nil {
					t.Fatal(err)
				}
				sc, scErr := e.sched.readSidecar(run.ID)
				if scErr != nil {
					t.Fatal(scErr)
				}
				continuation = "Continue on the restarted server."
				sc.AccountSwap = &accountSwap{From: e.member.ID, To: e.member.ID, Continuation: continuation, Container: sc.ContainerID}
				if err = e.sched.writeSidecar(sc); err != nil {
					t.Fatal(err)
				}
				cfg := e.cfg
				cfg.PTY = newFakePTY()
				cfg.PTY.(*fakePTY).logDir = e.pty.logDir
				if sched, err = New(cfg); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sched.Close() })
			}
			before := len(rt.all())
			// Coordination wakes an idle session whenever mail arrives. Idle
			// is read before the marker, which only the move's end clears.
			var early atomic.Bool
			go func() {
				for t.Context().Err() == nil {
					if len(rt.all()) > before && sched.IdleEnhanced(run.ID) {
						sched.mu.Lock()
						swap := sched.runs[run.ID].accountSwap
						early.Store(swap != nil && swap.Continuation != "")
						sched.mu.Unlock()
						_ = sched.WakeEnhanced(context.Background(), run.ID, "unread mail")
						return
					}
					goruntime.Gosched()
				}
			}()
			if after == "restart" {
				sched.UseCoordination(coord, binDir)
				startScheduler(t, sched)
			} else {
				_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			}
			waitFor(t, "the continuation, then the mail", func() bool {
				execs := rt.all()
				return len(execs) > before && len(sentPrompts(t, execs[len(execs)-1])) == 2
			})
			execs := rt.all()
			log, err := os.ReadFile(e.pty.ItemLogPath(run.ID))
			if got := sentPrompts(t, execs[len(execs)-1]); !slices.Equal(got, []string{continuation, "unread mail"}) || early.Load() || err != nil || strings.Contains(string(log), "went on without it") {
				t.Fatalf("the replacement was prompted %q, idle while owed %v (log read: %v); want the continuation, then the mail, and no skipped continuation", got, early.Load(), err)
			}
		})
	}
}

// Mail is not handed to the agent a move holds, which the move would stop
// mid-turn.
func TestMailDuringAMoveIsNotDelivered(t *testing.T) {
	t.Parallel()
	e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := rt.all()[0]
	var wakeErr error
	e.sched.UseUsageFallback(UsageFallback{Usage: &hookedLoginUsage{loginUsage: usage, during: func() {
		wakeErr = e.sched.WakeEnhanced(context.Background(), run.ID, "unread mail")
	}}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the session on the second login", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		return rowErr == nil && row.Account() == second
	})
	if !errors.Is(wakeErr, errACPBusy) || !slices.Equal(sentPrompts(t, first), []string{acpmock.PromptUsageLimit}) {
		t.Fatalf("wake during the move: %v; the replaced agent was prompted %q; want it refused", wakeErr, sentPrompts(t, first))
	}
}

// A continuation is not marked as being sent once another move holds the
// session: that move would stop the agent it went to.
func TestNoContinuationIsSentWhileAMoveHoldsTheSession(t *testing.T) {
	t.Parallel()
	e, _, _, _, _ := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	swap := &accountSwap{From: e.member.ID, To: e.member.ID, Continuation: "Continue."}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	entry.accountSwap, entry.moving = swap, true
	e.sched.mu.Unlock()
	if e.sched.acp.markSending(entry, swap) {
		t.Fatal("the continuation was marked as being sent while a move held the session")
	}
	e.sched.mu.Lock()
	entry.accountSwap, entry.moving = nil, false
	e.sched.mu.Unlock()
}

// recheckStore holds the first run read after armed until the next member
// read, which a move makes only after it tried to send its continuation.
type recheckStore struct {
	store.Store
	armed, held, released atomic.Bool
	release               chan struct{}
}

func (s *recheckStore) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	if s.armed.Load() && s.held.CompareAndSwap(false, true) {
		<-s.release
	}
	return s.Store.GetRun(ctx, id)
}

func (s *recheckStore) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	if s.held.Load() && s.released.CompareAndSwap(false, true) {
		close(s.release)
	}
	return s.Store.GetMember(ctx, id)
}

// A revoke sweep that claims the session as its move to Grace ends, before
// the move sends its continuation, and then finds the session no longer on
// the revoked account sends the continuation once instead: an Enhanced
// session is told of the move and gets its mail after that turn, and a
// Background session's continuation turn ends the run.
func TestARevokeRecheckThatKeepsTheSessionSendsItsContinuation(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.LaunchMode{domain.LaunchACP, domain.LaunchHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			st := &recheckStore{release: make(chan struct{})}
			e, rt, notes := newFallbackEnv(t, func(cfg *Config) {
				enhancedClaude(cfg)
				cfg.Harnesses["codex"] = HarnessSpec{HeadlessArgs: []string{"fake-agent", "-p", "{task}"}, ACPArgs: []string{"acp-mock"}}
				st.Store = cfg.Store
				cfg.Store = st
			})
			e.share(t)
			var usage fallback.QuotaReader = claudeUsage{e.owner.ID: 10}
			if mode == domain.LaunchHeadless {
				writeHomeFiles(t, e.ownerHome, ".codex/auth.json")
				endsBackgroundRuns(e.testEnv)
				usage = codexUsage{}
			}
			var once sync.Once
			e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &sync.Mutex{}, Note: func(ctx context.Context, run *domain.Run, body, key string) error {
				once.Do(func() {
					e.sched.mu.Lock()
					entry := e.sched.runs[run.ID]
					e.sched.mu.Unlock()
					st.armed.Store(true)
					go e.sched.leaveRevokedAccount(entry)
					for deadline := time.Now().Add(waitTimeout); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
						e.sched.mu.Lock()
						claimed := st.held.Load() && entry.moving
						e.sched.mu.Unlock()
						if claimed {
							return
						}
					}
				})
				return notes.post(ctx, run, body, key)
			}})
			var run *domain.Run
			var err error
			if mode == domain.LaunchACP {
				run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
				if err != nil {
					t.Fatal(err)
				}
				waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
				_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			} else if run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, acpmock.PromptUsageLimit, "codex", domain.LaunchHeadless); err != nil {
				t.Fatal(err)
			}
			e.sched.mu.Lock()
			entry := e.sched.runs[run.ID]
			e.sched.mu.Unlock()
			waitFor(t, "the continuation", func() bool {
				execs := rt.all()
				return len(execs) > 1 && len(sentPrompts(t, execs[len(execs)-1])) > 0
			})
			replacement := rt.all()[len(rt.all())-1]
			if mode == domain.LaunchHeadless {
				e.waitStoreStatus(t, run.ID, domain.RunCompleted)
			} else {
				waitFor(t, "the session idle", func() bool { return e.sched.IdleEnhanced(run.ID) })
				if err = e.sched.WakeEnhanced(t.Context(), run.ID, "unread mail"); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the mail", func() bool { return len(sentPrompts(t, replacement)) == 2 })
			}
			swap, _ := e.marker(entry)
			want := []string{continuedOnGrace}
			if mode == domain.LaunchACP {
				want = append(want, "unread mail")
			}
			if got := sentPrompts(t, replacement); !slices.Equal(got, want) || swap != nil {
				t.Fatalf("the replacement was prompted %q, marker %+v; want %q and no marker", got, swap, want)
			}
		})
	}
}

// A session that left Ada's account on a restriction and then ran out on
// Grace's lists only Grace's login as tried: Ada's was not spent, and her
// usage, which shows no reset, does not make the time unknown.
func TestALoginLeftOnARestrictionIsNotListedAsTried(t *testing.T) {
	t.Parallel()
	e, _, notes, usage, _ := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	restricted, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"Your seat type doesn't include usage credits"}`))
	if err = e.sched.ReportAgentState(t.Context(), run.ID, restricted); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the room note", func() bool { return len(notes.all()) == 1 })
	waitFor(t, "the session on Grace's account", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		return rowErr == nil && row.Account() == domain.AccountOf(e.owner.ID) && row.Status == domain.RunRunning
	})
	usage.set(domain.AccountOf(e.owner.ID), 100)
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, standardLimit(t)); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if want := "; logins tried: Grace; the first is usable again at " + usage.resets.UTC().Format(time.RFC3339); !strings.HasSuffix(got.Reason, want) {
		t.Fatalf("reason %q; want it to end %q", got.Reason, want)
	}
}

// Where nobody involved keeps added logins, the refusal of a message
// during a move names accounts only.
func TestAPromptDuringAMoveBetweenAccountsNamesNoLogin(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	var injectErr error
	e.sched.UseUsageFallback(UsageFallback{Usage: &hookedLoginUsage{loginUsage: newLoginUsage(), during: func() {
		_, injectErr = e.sched.Inject(context.Background(), run.ID, e.member.ID, domain.AgentPrompt{Text: "run a long tool"}, false, nil)
	}}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the session on Grace's account", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		return rowErr == nil && row.AccountMemberID == e.owner.ID
	})
	if !errors.Is(injectErr, ErrMoving) || !strings.HasSuffix(injectErr.Error(), "scheduler: the session is moving to another account; send the message when it finishes") {
		t.Fatalf("prompt during the move: %v; want it refused naming accounts only", injectErr)
	}
}
