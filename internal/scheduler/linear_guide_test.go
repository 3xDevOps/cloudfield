package scheduler

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunGuideCarriesTheLinkedLinearIssue(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "linked",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: "aether/run-linked", LinearIssueURL: "https://linear.app/acme/issue/ENG-123",
	}
	if err := e.db.CreateRun(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	g := e.sched.runGuide(t.Context(), r.ID, false, false, "")
	if g.LinearIssue != "ENG-123" || g.LinearIssueURL != r.LinearIssueURL {
		t.Fatalf("guide issue=%q url=%q", g.LinearIssue, g.LinearIssueURL)
	}
	if g := e.sched.runGuide(t.Context(), "", false, false, ""); g.LinearIssue != "" {
		t.Fatalf("unlinked guide issue=%q", g.LinearIssue)
	}
}
