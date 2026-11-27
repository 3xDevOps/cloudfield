package coord

import (
	"context"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestReportBodyReachesTheParentWithTheReport: a subsession's report body is
// stored whole and read by its parent in the report's own inbox message. A
// body over the cap, or on an outcome that is not mailed, is refused, and a
// retry of a key with another body is a conflict.
func TestReportBodyReachesTheParentWithTheReport(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	h.svc.cfg.Outcomes = &recordingOutcomes{}
	findings := "1. internal/x.go:12 loses a wake\n2. internal/y.go:40 double mails\n" + strings.Repeat("é", (protocol.CoordMaxReportBodyBytes-80)/2)
	send := func(outcome, body, key string) *protocol.Error {
		_, rpcErr := h.svc.CoordReport(ctx, child, protocol.CoordReportParams{Outcome: outcome, Summary: "2 findings", Body: body, IdempotencyKey: key})
		return rpcErr
	}
	for name, tc := range map[string]struct{ outcome, body, want string }{
		"over the cap": {protocol.CoordOutcomeSuccess, strings.Repeat("x", protocol.CoordMaxReportBodyBytes+1), "body is 12289 bytes and exceeds 12288"},
		"on waiting":   {protocol.CoordOutcomeWaiting, "x", "body is only valid with"},
		"on answered":  {protocol.CoordOutcomeAnswered, "x", "body is only valid with"},
	} {
		if rpcErr := send(tc.outcome, tc.body, name); rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.Contains(rpcErr.Message, tc.want) {
			t.Fatalf("report with a body %s = %v, want CodeInvalidParams %q", name, rpcErr, tc.want)
		}
	}
	if rpcErr := send(protocol.CoordOutcomeSuccess, findings, "review"); rpcErr != nil {
		t.Fatalf("report with a body: %v", rpcErr)
	}
	if rpcErr := send(protocol.CoordOutcomeSuccess, findings, "review"); rpcErr != nil {
		t.Fatalf("retry of the same report: %v", rpcErr)
	}
	if rpcErr := send(protocol.CoordOutcomeSuccess, "other findings", "review"); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
		t.Fatalf("the same key with another body = %v, want CodeConflict", rpcErr)
	}
	inbox, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
	if rpcErr != nil || len(inbox.Messages) != 1 {
		t.Fatalf("inbox = %+v, %v; want the one report", inbox, rpcErr)
	}
	if m := inbox.Messages[0]; m.Kind != protocol.CoordMessageKindReport || m.Body != "2 findings" || m.ReportBody != findings {
		t.Fatalf("report message = kind %q, body %q, report body of %d bytes; want the summary and the whole %d-byte body", m.Kind, m.Body, len(m.ReportBody), len(findings))
	}
}

// TestReportBodiesCountTowardTheInboxBatch: two reports with long bodies do
// not share a batch, so neither is printed past what a harness shows.
func TestReportBodiesCountTowardTheInboxBatch(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	h.svc.cfg.Outcomes = &recordingOutcomes{}
	long := strings.Repeat("finding\n", 1200)
	for _, outcome := range []string{protocol.CoordOutcomeBlocked, protocol.CoordOutcomeFailure} {
		if _, rpcErr := h.svc.CoordReport(ctx, child, protocol.CoordReportParams{Outcome: outcome, Summary: outcome, Body: long, IdempotencyKey: outcome}); rpcErr != nil {
			t.Fatalf("report %s: %v", outcome, rpcErr)
		}
	}
	first, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
	if rpcErr != nil || len(first.Messages) != 1 || first.Messages[0].ReportBody != long || first.More != 1 {
		t.Fatalf("first read = %d messages, more %d, %v; want one report and one more waiting", len(first.Messages), first.More, rpcErr)
	}
	second, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{AckToken: first.AckToken})
	if rpcErr != nil || len(second.Messages) != 1 || second.Messages[0].Body != protocol.CoordOutcomeFailure || second.Messages[0].ReportBody != long {
		t.Fatalf("second read = %+v, %v; want the other report whole", second.Messages, rpcErr)
	}
}
