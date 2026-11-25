package coord

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCoordReportIsRefusedWhileMailIsUnread(t *testing.T) {
	h := newHarness(t, 4, func(c *Config) { c.Evidence = &coordReportEvidenceCapture{id: "ev_mail"} })
	ctx := context.Background()
	from := h.run(0)
	send := func(to domain.RunID) {
		t.Helper()
		h.peers.pair(from, to, "src/auth.go")
		if _, err := h.svc.Send(ctx, from, sendParams(to, "scope changed")); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	refused := func(run domain.RunID, key string) {
		t.Helper()
		want := fmt.Sprintf("coord.report: 1 unread message (from %s). Run aether-internal inbox, handle them, then report again.", from)
		_, err := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
			Outcome: protocol.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: key,
		})
		if err == nil || err.Code != protocol.CodeInvalidState || err.Message != want {
			t.Fatalf("report with unread mail = %v, want CodeInvalidState %q", err, want)
		}
	}

	read := h.run(1)
	send(read)
	refused(read, "read-1")
	refused(read, "read-2")
	if _, err := h.svc.Inbox(ctx, read, protocol.CoordInboxParams{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	report(t, h, read, protocol.CoordOutcomeSuccess, "done", "read-3")

	// A denied inbox call still counts, and so does mail that arrived after it.
	denied := h.run(2)
	send(denied)
	refused(denied, "denied-1")
	if _, err := h.svc.Inbox(ctx, denied, protocol.CoordInboxParams{WaitSeconds: -1}); err == nil {
		t.Fatal("Inbox with a negative wait succeeded")
	}
	send(denied)
	report(t, h, denied, protocol.CoordOutcomeSuccess, "done", "denied-2")

	// The pass an inbox call buys ends with any other report.
	if _, err := h.svc.Inbox(ctx, read, protocol.CoordInboxParams{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	send(read)
	refused(read, "read-4")
	if _, err := h.svc.Inbox(ctx, read, protocol.CoordInboxParams{WaitSeconds: -1}); err == nil {
		t.Fatal("Inbox with a negative wait succeeded")
	}
	report(t, h, read, protocol.CoordOutcomeBlocked, "needs access", "read-5")
	refused(read, "read-6")
	h.advance(time.Minute)

	acker := h.run(3)
	send(acker)
	batch, err := h.svc.Inbox(ctx, acker, protocol.CoordInboxParams{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	send(acker)
	for _, key := range []string{"ack-1", "ack-2"} {
		acked, err := h.svc.Inbox(ctx, acker, protocol.CoordInboxParams{AckToken: batch.AckToken, AckOnly: true})
		if err != nil || acked.Acked != (key == "ack-1") || acked.More != 1 || len(acked.Messages) != 0 || acked.AckToken != "" {
			t.Fatalf("ack-only inbox = %+v (err %v), want the next message left undelivered", acked, err)
		}
		refused(acker, key)
	}
}
