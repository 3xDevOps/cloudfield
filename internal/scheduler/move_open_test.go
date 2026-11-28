package scheduler

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/ctxlock"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// allPrompts is every prompt the run's agents were sent, in start order.
func allPrompts(t *testing.T, rt *acpRuntime) []string {
	t.Helper()
	var texts []string
	for _, exec := range rt.all() {
		texts = append(texts, sentPrompts(t, exec)...)
	}
	return texts
}

// A session whose launch never opened a conversation has none to continue:
// the agent a revoke moves it to gets the task, once, and no continuation,
// and nothing stays owed.
func TestAMoveOfASessionThatNeverOpenedGivesTheTask(t *testing.T) {
	t.Parallel()
	for _, by := range []string{"account", "login"} {
		t.Run(by, func(t *testing.T) {
			t.Parallel()
			e, rt, _, _, _ := newLoginMoveEnv(t, enhancedClaude)
			e.share(t)
			var opts domain.LaunchOptions
			work := domain.AccountOf(e.owner.ID)
			if by == "login" {
				work = e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
				opts.AccountLogin = work.Login
			}
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			rt.mu.Lock()
			rt.startErr = errors.New("acpRuntime: the agent is not installed")
			rt.mu.Unlock()
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "fix it", "claude", domain.LaunchACP, opts)
			if err != nil {
				t.Fatal(err)
			}
			e.opened(t, run.ID)
			if e.sched.acp.openError(run.ID) == nil {
				t.Fatal("the launch's agent opened; want its open failed")
			}
			rt.mu.Lock()
			rt.startErr = nil
			rt.mu.Unlock()
			if by == "login" {
				if err = e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", work.Login, false); err != nil {
					t.Fatal(err)
				}
				e.sched.RevokeLogin(work)
			} else {
				e.revoke(t)
			}
			waitFor(t, "the session moved and opened", func() bool {
				row, rowErr := e.db.GetRun(t.Context(), run.ID)
				sc, scErr := e.sched.readSidecar(run.ID)
				return rowErr == nil && scErr == nil && row.Account() != work && sc.AccountSwap == nil && e.sched.acp.session(run.ID) != nil
			})
			time.Sleep(300 * time.Millisecond)
			if got := allPrompts(t, rt); len(got) != 1 || !strings.HasPrefix(got[0], "fix it") {
				t.Fatalf("the agents were prompted %q; want the task once and no continuation", got)
			}
			if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status.Terminal() {
				t.Fatalf("run = %+v, %v; want it live", row, err)
			}
		})
	}
}

// A server that stops while the agent a move started is opening leaves the
// move's continuation owed, and the next server process sends it once.
func TestAShutdownDuringAMovesOpenLeavesItsContinuationOwed(t *testing.T) {
	t.Parallel()
	e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	stopped := make(chan struct{})
	var once sync.Once
	rt.mu.Lock()
	rt.onAttach = func() error {
		attachErr := error(nil)
		once.Do(func() {
			go func() {
				_ = e.sched.Close()
				close(stopped)
			}()
			<-e.sched.superCtx.Done()
			attachErr = e.sched.superCtx.Err()
			rt.mu.Lock()
			rt.startErr = attachErr
			rt.mu.Unlock()
		})
		return attachErr
	}
	rt.mu.Unlock()
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	<-stopped
	if sc, scErr := e.sched.readSidecar(run.ID); scErr != nil || sc.AccountSwap == nil || sc.AccountSwap.Continuation != continuedOnSecond || sc.AccountSwap.Sending {
		t.Fatalf("marker after the stop: %+v, %v; want the continuation owed", sc.AccountSwap, scErr)
	}
	rt.mu.Lock()
	rt.startErr = nil
	rt.mu.Unlock()

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &ctxlock.Mutex{}, Note: notes.post})
	startScheduler(t, s2)
	continuations := func() int {
		return len(slices.DeleteFunc(allPrompts(t, rt), func(text string) bool { return text != continuedOnSecond }))
	}
	waitFor(t, "the owed continuation", func() bool { return continuations() == 1 })
	waitFor(t, "the marker cleared", func() bool {
		sc, scErr := s2.readSidecar(run.ID)
		return scErr == nil && sc.AccountSwap == nil
	})
	time.Sleep(200 * time.Millisecond)
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status.Terminal() || row.Account() != second || continuations() != 1 {
		t.Fatalf("run = %+v, %v, %d continuations; want it live on the second login, continued once", row, err, continuations())
	}
}
