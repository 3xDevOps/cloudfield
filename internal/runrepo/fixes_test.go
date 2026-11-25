package runrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCreatePRAddsFixesLineForLinkedIssue(t *testing.T) {
	for _, tc := range []struct{ name, issue, body, want string }{
		{"appended", "ENG-123", "Reviewed", "Reviewed\n\nFixes ENG-123"},
		{"empty body", "ENG-123", "", "Fixes ENG-123"},
		{"already named", "ENG-123", "Closes ENG-123 too", "Closes ENG-123 too"},
		{"unlinked", "", "Reviewed", "Reviewed"},
		{"longer identifier", "ENG-12", "Fixes ENG-123", "Fixes ENG-123\n\nFixes ENG-12"},
		{"longer identifier in URL", "ENG-12", "https://linear.app/acme/issue/ENG-120", "https://linear.app/acme/issue/ENG-120\n\nFixes ENG-12"},
		{"identifier in URL", "ENG-12", "https://linear.app/acme/issue/ENG-12", "https://linear.app/acme/issue/ENG-12"},
		{"before trailers", "ENG-1", "Summary\n\nCo-authored-by: A <a@x>", "Summary\n\nFixes ENG-1\n\nCo-authored-by: A <a@x>"},
		{"trailers only", "ENG-1", "Co-authored-by: A <a@x>", "Fixes ENG-1\n\nCo-authored-by: A <a@x>"},
		{"co-authors added once", "ENG-1", "Summary\n\nCo-authored-by: A <a@x>", "Summary\n\nFixes ENG-1\n\nCo-authored-by: A <a@x>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, run, expected := newRepo(t)
			run.LinearIssue = tc.issue
			if tc.name == "co-authors added once" {
				run.CoAuthors = func(context.Context, string) ([]string, error) {
					return []string{"Co-authored-by: A <a@x>"}, nil
				}
			}
			f := newGitHubFixture(expected.Head)
			s.exec = githubExec(t, f)
			if _, err := s.CreatePR(t.Context(), run, PRCreateRequest{Expected: expected, Target: f.target, Title: "Publish", Body: tc.body}); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.body != tc.want {
				t.Fatalf("body=%q want %q", f.body, tc.want)
			}
		})
	}
}

func TestWithFixesRejectsBodyThatOverflowsWithTheLine(t *testing.T) {
	if _, err := withFixes("ENG-1", strings.Repeat("x", protocol.MaxRunPRBodyBytes)); err == nil {
		t.Fatal("expected the input limit error")
	}
}
