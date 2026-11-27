package coord

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestSubsessionReportRepeat: a subsession's report is mailed to its parent
// unless it says again, word for word, what its report before said less
// than repeatWindow earlier, with no mail read in between.
func TestSubsessionReportRepeat(t *testing.T) {
	ctx := context.Background()
	success, failure, blocked := protocol.CoordOutcomeSuccess, protocol.CoordOutcomeFailure, protocol.CoordOutcomeBlocked
	type sent struct{ outcome, summary string }
	for _, tc := range []struct {
		name   string
		window time.Duration
		// between runs after the first report.
		between func(t *testing.T, h *coordHarness, child, parent domain.RunID)
		reports []sent
		want    []string
	}{
		{name: "the same words again are a repeat", reports: []sent{{success, "done"}, {success, "done"}, {success, "done"}}, want: []string{"done"}},
		{name: "blocked with the same words again is a repeat", reports: []sent{{blocked, "no token"}, {blocked, "no token"}}, want: []string{"no token"}},
		{name: "other words are sent", reports: []sent{{success, "done"}, {success, "done, and #7 merged"}}, want: []string{"done", "done, and #7 merged"}},
		{name: "another outcome is sent", reports: []sent{{success, "x"}, {failure, "x"}, {failure, "x"}, {success, "x"}}, want: []string{"x", "x", "x"}},
		{name: "the same words after the window are sent", window: time.Nanosecond, reports: []sent{{success, "done"}, {success, "done"}}, want: []string{"done", "done"}},
		{name: "the same words after mail was read are sent", reports: []sent{{blocked, "no token"}, {blocked, "no token"}}, want: []string{"no token", "no token"},
			between: func(t *testing.T, h *coordHarness, child, parent domain.RunID) {
				if _, err := h.svc.Send(ctx, parent, sendParams(child, "try the staging one")); err != nil {
					t.Fatalf("send: %v", err)
				}
				if _, err := h.svc.Inbox(ctx, child, protocol.CoordInboxParams{}); err != nil {
					t.Fatalf("inbox: %v", err)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.window != 0 {
				window := repeatWindow
				repeatWindow = tc.window
				t.Cleanup(func() { repeatWindow = window })
			}
			h := newHarness(t, 2, func(c *Config) {
				c.Evidence = &coordReportEvidenceCapture{id: "repeat-packet"}
				c.Outcomes = &recordingOutcomes{}
			})
			child, parent := h.run(0), h.run(1)
			h.svc.cfg.Children = parentOf{child: child, parent: parent}
			for i, r := range tc.reports {
				report(t, h, child, r.outcome, r.summary, "key-"+string(rune('1'+i)))
				if i == 0 && tc.between != nil {
					tc.between(t, h, child, parent)
				}
			}
			inbox, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
			if rpcErr != nil {
				t.Fatalf("inbox: %v", rpcErr)
			}
			var got []string
			for _, m := range inbox.Messages {
				got = append(got, m.Body)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("parent was mailed %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProvisionalReportNamesWhatTheTurnReported: a turn that reported only
// that it replied is forwarded as that, not as a turn without a report.
func TestProvisionalReportNamesWhatTheTurnReported(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	h.svc.cfg.Outcomes = &recordingOutcomes{}
	turn := time.Now().UTC()
	report(t, h, child, protocol.CoordOutcomeAnswered, "explained the retry policy", "answered")
	if err := h.svc.ProvisionalReport(ctx, child, turn, "The retry policy is in docs/retry.md."); err != nil {
		t.Fatalf("ProvisionalReport: %v", err)
	}
	inbox, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
	want := "Provisional report: this session ended its turn after reporting answered. " +
		"A success, failure or blocked report from it supersedes this one. Its last message:\n\nThe retry policy is in docs/retry.md."
	if rpcErr != nil || len(inbox.Messages) != 1 || inbox.Messages[0].Body != want {
		t.Fatalf("inbox = %+v, %v; want %q", inbox, rpcErr, want)
	}
}

// TestAnsweredAfterSuccessChangesNothing: a turn that reported success and
// then that it replied keeps its success; the scheduler is not told.
func TestAnsweredAfterSuccessChangesNothing(t *testing.T) {
	outcomes := &recordingOutcomes{since: time.Now().UTC()}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "answered-packet"}
		c.Outcomes = outcomes
	})
	run := h.run(0)
	report(t, h, run, protocol.CoordOutcomeSuccess, "merged #7", "done")
	report(t, h, run, protocol.CoordOutcomeAnswered, "said so", "replied")
	outcomes.mu.Lock()
	defer outcomes.mu.Unlock()
	if len(outcomes.calls) != 1 || outcomes.calls[0].status != domain.RunCompleted {
		t.Fatalf("scheduler got %+v, want only the success", outcomes.calls)
	}
}
