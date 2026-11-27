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
		{name: "the same words after another message to the parent are sent", reports: []sent{{success, "done"}, {success, "done"}}, want: []string{"done", "wait, one test fails", "done"},
			between: func(t *testing.T, h *coordHarness, child, parent domain.RunID) {
				if _, err := h.svc.Send(ctx, child, sendParams(parent, "wait, one test fails")); err != nil {
					t.Fatalf("send: %v", err)
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

// TestAnsweredAfterSuccess: a turn that reported success and then that it
// replied keeps its success, and the scheduler is not told. An answered
// report in a turn the scheduler does not know, in a later turn, after the
// window, or after a blocked report is handed to the scheduler as before.
func TestAnsweredAfterSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  string
		window time.Duration
		// turn is when the turn started, relative to the first report.
		turn func(firstAt time.Time) time.Time
		// waits reports waiting between the two.
		waits   bool
		ignored bool
	}{
		{name: "after a waiting report in between it applies", first: protocol.CoordOutcomeSuccess, waits: true,
			turn: func(at time.Time) time.Time { return at.Add(-time.Minute) }},
		{name: "in the same turn it is ignored", first: protocol.CoordOutcomeSuccess, ignored: true,
			turn: func(at time.Time) time.Time { return at.Add(-time.Minute) }},
		{name: "after a failure in the same turn it is ignored", first: protocol.CoordOutcomeFailure, ignored: true,
			turn: func(at time.Time) time.Time { return at.Add(-time.Minute) }},
		{name: "in an unknown turn it applies", first: protocol.CoordOutcomeSuccess,
			turn: func(time.Time) time.Time { return time.Time{} }},
		{name: "in a later turn it applies", first: protocol.CoordOutcomeSuccess,
			turn: func(at time.Time) time.Time { return at.Add(time.Millisecond) }},
		{name: "after the window it applies", first: protocol.CoordOutcomeSuccess, window: time.Nanosecond,
			turn: func(at time.Time) time.Time { return at.Add(-time.Minute) }},
		{name: "after a blocked report it is left to the scheduler", first: protocol.CoordOutcomeBlocked,
			turn: func(at time.Time) time.Time { return at.Add(-time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.window != 0 {
				window := repeatWindow
				repeatWindow = tc.window
				t.Cleanup(func() { repeatWindow = window })
			}
			outcomes := &recordingOutcomes{}
			h := newHarness(t, 1, func(c *Config) {
				c.Evidence = &coordReportEvidenceCapture{id: "answered-packet"}
				c.Outcomes = outcomes
			})
			run := h.run(0)
			report(t, h, run, tc.first, "merged #7", "done")
			outcomes.mu.Lock()
			outcomes.since = tc.turn(time.Now().UTC())
			outcomes.mu.Unlock()
			want := 1
			if tc.waits {
				report(t, h, run, protocol.CoordOutcomeWaiting, "on CI", "waiting")
				want = 2
			}
			report(t, h, run, protocol.CoordOutcomeAnswered, "said so", "replied")
			calls := outcomes.recorded()
			if got := len(calls) == want; got != tc.ignored {
				t.Fatalf("scheduler got %+v; answered ignored = %v, want %v", calls, got, tc.ignored)
			}
		})
	}
}
