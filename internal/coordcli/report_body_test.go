package coordcli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// A server older than report bodies accepts a report and drops the body it
// does not know. The command must say so instead of reporting success.
func TestReportSaysWhenTheServerDroppedItsBody(t *testing.T) {
	if err := protocol.CheckReportBody("findings", protocol.CoordReportResult{ReportID: "rpt-1", Outcome: "success"}); err == nil ||
		!strings.Contains(err.Error(), "WITHOUT its body") {
		t.Fatalf("a receipt with no body size = %v, want the dropped-body error", err)
	}
	if err := protocol.CheckReportBody("findings", protocol.CoordReportResult{BodyBytes: 8}); err != nil {
		t.Fatalf("a receipt with the body's size = %v", err)
	}
	if err := protocol.CheckReportBody("", protocol.CoordReportResult{}); err != nil {
		t.Fatalf("a report without a body = %v", err)
	}
}

// A flag after a positional summary would be read as summary text, and a
// body named there would be dropped without a word.
func TestReportRefusesAFlagAfterThePositionalSummary(t *testing.T) {
	for _, args := range [][]string{
		{"success", "two", "findings", "--body-file", "/tmp/findings"},
		{"success", "done", "-body=x"},
	} {
		_, err := report(context.Background(), "/nonexistent.sock", args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "after the summary; put flags before it") {
			t.Fatalf("report %q = %v, want the flag refused", args, err)
		}
	}
	// A summary may still contain a word that only looks like a flag.
	_, err := report(context.Background(), "/nonexistent.sock", []string{"success", "tests", "pass", "with", "-race"}, strings.NewReader(""), io.Discard)
	if err == nil || strings.Contains(err.Error(), "after the summary") {
		t.Fatalf("report with -race in its summary = %v, want it to reach the socket", err)
	}
}
