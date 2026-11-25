package runrepo

import "testing"

func TestCreatePRAddsFixesLineForLinkedIssue(t *testing.T) {
	for _, tc := range []struct{ name, issue, body, want string }{
		{"appended", "ENG-123", "Reviewed", "Reviewed\n\nFixes ENG-123"},
		{"empty body", "ENG-123", "", "Fixes ENG-123"},
		{"already named", "ENG-123", "Closes ENG-123 too", "Closes ENG-123 too"},
		{"unlinked", "", "Reviewed", "Reviewed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, run, expected := newRepo(t)
			run.LinearIssue = tc.issue
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
