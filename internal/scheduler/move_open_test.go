package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"

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
