package coord

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

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
	want := "Provisional report: this session ended its turn with no success, failure or blocked report; its last report was answered. " +
		"A success, failure or blocked report from it supersedes this one. Its last message:\n\nThe retry policy is in docs/retry.md."
	if rpcErr != nil || len(inbox.Messages) != 1 || inbox.Messages[0].Body != want {
		t.Fatalf("inbox = %+v, %v; want %q", inbox, rpcErr, want)
	}
}
