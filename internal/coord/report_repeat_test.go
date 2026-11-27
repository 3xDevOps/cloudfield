package coord

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestSubsessionReportRepeat: which of a subsession's reports its parent is
// mailed. Only a report with the outcome of the one before it, made within
// repeatWindow in a stretch of work with no mail read and no needs-input,
// waiting or answered report, is a repeat the parent is not sent again.
func TestSubsessionReportRepeat(t *testing.T) {
	ctx := context.Background()
	success, failure, blocked := protocol.CoordOutcomeSuccess, protocol.CoordOutcomeFailure, protocol.CoordOutcomeBlocked
	for _, tc := range []struct {
		name string
		// unknown leaves the stretch unknown, as it is after a member's
		// message and for an agent that reports no turns.
		unknown bool
		window  time.Duration
		// between runs after the first report.
		between func(t *testing.T, h *coordHarness, child, parent domain.RunID, outcomes *recordingOutcomes)
		reports []string
		want    []string
	}{
		{name: "the same outcome again is a repeat", reports: []string{success, success, success}, want: []string{"1"}},
		{name: "blocked again is a repeat", reports: []string{blocked, blocked}, want: []string{"1"}},
		{name: "another outcome is sent", reports: []string{success, failure, failure, success}, want: []string{"1", "2", "4"}},
		{name: "a report after the window is sent", window: time.Nanosecond, reports: []string{success, success}, want: []string{"1", "2"}},
		{name: "a report in an unknown stretch is sent", unknown: true, reports: []string{success, success}, want: []string{"1", "2"}},
		{name: "a report after a new stretch began is sent", reports: []string{success, success}, want: []string{"1", "2"},
			between: func(_ *testing.T, _ *coordHarness, _, _ domain.RunID, outcomes *recordingOutcomes) {
				outcomes.since = time.Now().UTC()
			}},
		{name: "a report after mail was read is sent", reports: []string{success, success}, want: []string{"1", "2"},
			between: func(t *testing.T, h *coordHarness, child, parent domain.RunID, _ *recordingOutcomes) {
				if _, err := h.svc.Send(ctx, parent, sendParams(child, "one more thing")); err != nil {
					t.Fatalf("send: %v", err)
				}
				if _, err := h.svc.Inbox(ctx, child, protocol.CoordInboxParams{}); err != nil {
					t.Fatalf("inbox: %v", err)
				}
			}},
		{name: "a report after a waiting report is sent", reports: []string{success, success}, want: []string{"1", "2"},
			between: func(t *testing.T, h *coordHarness, child, _ domain.RunID, _ *recordingOutcomes) {
				report(t, h, child, protocol.CoordOutcomeWaiting, "on CI", "waiting")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.window != 0 {
				window := repeatWindow
				repeatWindow = tc.window
				t.Cleanup(func() { repeatWindow = window })
			}
			outcomes := &recordingOutcomes{}
			if !tc.unknown {
				outcomes.since = time.Now().UTC()
			}
			h := newHarness(t, 2, func(c *Config) {
				c.Evidence = &coordReportEvidenceCapture{id: "repeat-packet"}
				c.Outcomes = outcomes
			})
			child, parent := h.run(0), h.run(1)
			h.svc.cfg.Children = parentOf{child: child, parent: parent}
			for i, outcome := range tc.reports {
				n := string(rune('1' + i))
				report(t, h, child, outcome, n, "key-"+n)
				if i == 0 && tc.between != nil {
					tc.between(t, h, child, parent, outcomes)
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
				t.Fatalf("parent was mailed reports %q, want %q", got, tc.want)
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
