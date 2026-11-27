package coordcli

import (
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
