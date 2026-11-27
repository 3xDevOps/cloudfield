package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// TestAnsweredDoesNotReplaceTheTurnsOutcome: a turn that reported success or
// failure and then that it replied keeps its outcome, however often it says
// so. An answered report applies as before in a turn the scheduler has no
// start for, in a later turn, and after a waiting report.
func TestAnsweredDoesNotReplaceTheTurnsOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		outcome domain.RunStatus
		// between runs after the outcome report; turn says whether the
		// scheduler was told the turn started.
		turn    bool
		between func(t *testing.T, e *testEnv, run domain.RunID)
		want    string
	}{
		{name: "after success", outcome: domain.RunCompleted, turn: true, want: reportedSuccessReason},
		{name: "after failure", outcome: domain.RunFailed, turn: true, want: reportedFailureReason},
		{name: "in a turn with no known start", outcome: domain.RunCompleted, want: answeredReasonPrefix + "said so"},
		{name: "in a later turn", outcome: domain.RunCompleted, turn: true, want: answeredReasonPrefix + "said so",
			between: func(t *testing.T, e *testEnv, run domain.RunID) {
				for _, state := range []agentstatus.Report{{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}, {State: agentstatus.Working, TurnStart: true}} {
					if err := e.sched.ReportAgentState(t.Context(), run, state); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "after a waiting report", outcome: domain.RunCompleted, turn: true, want: answeredReasonPrefix + "said so",
			between: func(t *testing.T, e *testEnv, run domain.RunID) {
				if err := e.sched.ReportWaiting(t.Context(), run, "waiting-1", "", "on CI", time.Now()); err != nil {
					t.Fatal(err)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newReportingEnv(t, nil)
			ctx := t.Context()
			run, _ := e.launchReporting(t)
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			if tc.turn {
				if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working, TurnStart: true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.sched.FinishReported(ctx, run.ID, "final-1", tc.outcome, time.Now()); err != nil {
				t.Fatal(err)
			}
			if tc.between != nil {
				tc.between(t, e, run.ID)
			}
			for _, id := range []string{"answered-1", "answered-2"} {
				if err := e.sched.ReportAnswered(ctx, run.ID, id, "said so", time.Now()); err != nil {
					t.Fatalf("ReportAnswered %s: %v", id, err)
				}
			}
			e.sched.mu.Lock()
			got := e.sched.runs[run.ID].idleReason
			e.sched.mu.Unlock()
			if got != tc.want {
				t.Fatalf("the session's outcome is %q, want %q", got, tc.want)
			}
		})
	}
}
