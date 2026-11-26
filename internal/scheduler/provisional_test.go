package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func shortProvisionalGrace(t *testing.T) {
	grace := provisionalGrace
	provisionalGrace = time.Second
	t.Cleanup(func() { provisionalGrace = grace })
}

func launchChildRun(t *testing.T, e *testEnv, parent *domain.Run, harness string, mode domain.LaunchMode) *domain.Run {
	t.Helper()
	reserved, _, err := e.db.ReserveChild(t.Context(), &domain.ChildReservation{
		ParentRunID: parent.ID, RootRunID: parent.ID, TreeKey: string(parent.ID), WorkspaceID: e.ws.ID,
		OwnerID: e.member.ID, AccountID: e.member.ID, Harness: harness, Mode: mode,
		Purpose: "say pong", Task: "say pong", IdempotencyKey: "k", RequestHash: "h",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", harness, mode,
		domain.LaunchOptions{ReservedRunID: reserved.RunID})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestEnhancedChildWokenWithinTheGraceForwardsOnlyItsLastTurn(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, _ := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	var wake sync.Once
	c.onWake = func(run domain.RunID) {
		if run == parent.ID {
			return
		}
		wake.Do(func() {
			go func() {
				if err := e.sched.WakeEnhanced(t.Context(), run, "say pong"); err != nil {
					t.Errorf("WakeEnhanced: %v", err)
				}
			}()
		})
	}
	run := launchChildRun(t, e, parent, "fake", domain.LaunchACP)
	waitItems(t, e.sched, run.ID, "both turns", turnEnded("end_turn", 2))
	waitFor(t, "a provisional report", func() bool { return len(c.provisionalReports()) > 0 })
	e.sched.mu.Lock()
	last := e.sched.runs[run.ID].turnStartedAt
	e.sched.mu.Unlock()
	if calls := c.provisionalReports(); !calls[0].since.Equal(last) {
		t.Fatalf("provisional report for the turn started %v, want only the last turn's (%v)", calls[0].since, last)
	}
}

func TestEnhancedChildRunForwardsItsLastMessageToItsParent(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, _ := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	child := launchChildRun(t, e, parent, "fake", domain.LaunchACP)
	waitFor(t, "the child's last message", func() bool {
		for _, call := range c.provisionalReports() {
			if call.run == child.ID && strings.Contains(call.message, "pong") {
				return true
			}
		}
		return false
	})
	e.waitStoreStatus(t, child.ID, domain.RunNeedsAttention)
	time.Sleep(provisionalGrace / 2)
	if calls := c.provisionalReports(); len(calls) != 1 {
		t.Fatalf("provisional reports %+v, want the turn forwarded once", calls)
	}
}

// A Standard subsession's container is marked as one, and its turn end is
// forwarded once, with the agent's last message: that of the stop held for a
// report when one was held. A session with no parent forwards nothing.
func TestStandardChildRunForwardsItsTurnEndToItsParent(t *testing.T) {
	shortProvisionalGrace(t)
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	c, _ := withCoordination(t, e)
	parent, parentContainer := e.launchFake(t, "parent")
	child := launchChildRun(t, e, parent, "fake", domain.LaunchTUI)
	if own, sub := parentContainer.spec.Env[coordtransport.SubsessionEnv], e.rt.byName(string(child.ID)).spec.Env[coordtransport.SubsessionEnv]; own != "" || sub != "1" {
		t.Fatalf("%s is %q in the parent and %q in the subsession, want unset and 1", coordtransport.SubsessionEnv, own, sub)
	}
	report := func(run domain.RunID, report agentstatus.Report) {
		t.Helper()
		if err := e.sched.ReportAgentState(t.Context(), run, report); err != nil {
			t.Fatal(err)
		}
	}
	working := agentstatus.Report{State: agentstatus.Working, TurnStart: true}
	idle := func(message string) agentstatus.Report {
		return agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle, LastMessage: message}
	}
	forwarded := func() []string {
		var messages []string
		for _, call := range c.provisionalReports() {
			if call.run != child.ID {
				t.Fatalf("provisional report from %s, want only the subsession %s", call.run, child.ID)
			}
			messages = append(messages, call.message)
		}
		return messages
	}

	report(parent.ID, working)
	report(parent.ID, idle("the parent's own"))
	report(child.ID, working)
	if reminder, err := e.sched.HoldTurnEnd(t.Context(), child.ID, "pong"); err != nil || reminder == "" {
		t.Fatalf("HoldTurnEnd = %q, %v; want the reminder", reminder, err)
	}
	report(child.ID, idle("I have nothing to report"))
	// The session's end repeats the idle report.
	report(child.ID, idle(""))
	waitFor(t, "the held stop's message", func() bool { return len(forwarded()) == 1 })

	report(child.ID, working)
	report(child.ID, idle("ping"))
	waitFor(t, "the next turn's message", func() bool { return len(forwarded()) == 2 })
	time.Sleep(provisionalGrace / 2)
	if got := forwarded(); !slices.Equal(got, []string{"pong", "ping"}) {
		t.Fatalf("forwarded %q, want each turn's message once", got)
	}
}

func TestEnhancedChildRunForwardsATurnItsKeeperEndedDuringARestart(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, binDir := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	child := launchChildRun(t, e, parent, "fake", domain.LaunchACP)
	waitItems(t, e.sched, child.ID, "the child's first turn", turnEnded("end_turn", 1))
	release := filepath.Join(t.TempDir(), "release")
	prompt := acpmock.PromptRunPrefix + "while [ ! -e " + release + " ]; do sleep 0.02; done; echo replayed"
	if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: prompt}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child's second turn", func() bool {
		sess := e.sched.acp.session(child.ID)
		return sess != nil && sess.State().TurnInFlight
	})
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	// The turn ends while no server is attached; the keeper replays its end.
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	// The adopted session then records its id after it opens, slowly, so the
	// keeper's replay ends the turn before the session is published.
	if err := e.db.SetRunAgentSession(t.Context(), child.ID, ""); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Store = slowAgentSessionStore{e.cfg.Store}
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	next.UseCoordination(c, binDir)
	startScheduler(t, next)
	waitFor(t, "the replayed turn's message at the parent", func() bool {
		for _, call := range c.provisionalReports() {
			if call.run == child.ID && strings.Contains(call.message, "replayed") {
				return true
			}
		}
		return false
	})
}

type slowAgentSessionStore struct{ store.Store }

func (s slowAgentSessionStore) SetRunAgentSession(ctx context.Context, id domain.RunID, session string) error {
	time.Sleep(time.Second)
	return s.Store.SetRunAgentSession(ctx, id, session)
}
