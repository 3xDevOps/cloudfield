package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// TestReportsSince: the stretch of work a session's reports are compared
// within is unknown until its agent reports a turn start and after a
// member's message, starts again with each turn, and moves when the agent
// stops working on its prompt.
func TestReportsSince(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	state := func(report agentstatus.Report) time.Time {
		t.Helper()
		if err := e.sched.ReportAgentState(ctx, run.ID, report); err != nil {
			t.Fatal(err)
		}
		return e.sched.ReportsSince(run.ID)
	}
	if since := e.sched.ReportsSince(run.ID); !since.IsZero() {
		t.Fatalf("stretch began %v before the agent reported a turn", since)
	}
	started := state(agentstatus.Report{State: agentstatus.Working, TurnStart: true})
	if started.IsZero() {
		t.Fatal("a turn start began no stretch")
	}
	if since := state(agentstatus.Report{State: agentstatus.Working}); !since.Equal(started) {
		t.Fatalf("a tool call moved the stretch from %v to %v", started, since)
	}
	if _, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "also bump the version"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if since := state(agentstatus.Report{State: agentstatus.Working}); !since.IsZero() {
		t.Fatalf("stretch still began %v after a member's message", since)
	}
	next := state(agentstatus.Report{State: agentstatus.Working, TurnStart: true})
	if !next.After(started) {
		t.Fatalf("the next turn's stretch began %v, want later than %v", next, started)
	}
	if since := state(permissionInput("open")); !since.IsZero() {
		t.Fatalf("stretch still began %v after the agent asked a member", since)
	}
	if since := state(permissionInput("close")); !since.IsZero() {
		t.Fatalf("stretch still began %v after a member answered", since)
	}
	next = state(agentstatus.Report{State: agentstatus.Working, TurnStart: true})
	if next.IsZero() {
		t.Fatal("the turn after a member's answer began no stretch")
	}
	if since := state(agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); !since.After(next) {
		t.Fatalf("the turn's end left the stretch at %v", since)
	}
}
