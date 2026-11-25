package linear

import "testing"

func TestParseIssueURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw, id, canonical string
		bad                bool
	}{
		{raw: "https://linear.app/acme/issue/ENG-123", id: "ENG-123", canonical: "https://linear.app/acme/issue/ENG-123"},
		{raw: "https://linear.app/acme/issue/eng-123/fix-the-login-test", id: "ENG-123", canonical: "https://linear.app/acme/issue/ENG-123"},
		{raw: "https://linear.app/acme/issue/ENG-7/slug/?x=1#c", id: "ENG-7", canonical: "https://linear.app/acme/issue/ENG-7"},
		{raw: "http://linear.app/acme/issue/ENG-1", bad: true},
		{raw: "https://example.com/acme/issue/ENG-1", bad: true},
		{raw: "https://linear.app/acme/project/ENG-1", bad: true},
		{raw: "https://linear.app/acme/issue/ENG", bad: true},
		{raw: "https://linear.app/acme/issue/ENG-1/a/b", bad: true},
		{raw: "https://linear.app/issue/ENG-1", bad: true},
		{raw: "", bad: true},
	} {
		id, canonical, err := ParseIssueURL(tc.raw)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseIssueURL(%q) = %q, want an error", tc.raw, canonical)
			}
			continue
		}
		if err != nil || id != tc.id || canonical != tc.canonical {
			t.Errorf("ParseIssueURL(%q) = %q, %q, %v; want %q, %q", tc.raw, id, canonical, err, tc.id, tc.canonical)
		}
	}
}

func TestFindIssueURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, want string }{
		{"fix https://linear.app/acme/issue/ENG-9/login.", "https://linear.app/acme/issue/ENG-9"},
		{"(see https://linear.app/acme/issue/ENG-9)", "https://linear.app/acme/issue/ENG-9"},
		{"https://linear.app/acme/project/x then https://linear.app/acme/issue/ENG-2 and ENG-3", "https://linear.app/acme/issue/ENG-2"},
		{"https://linear.app/acme/issue/ENG-1 https://linear.app/acme/issue/ENG-2", "https://linear.app/acme/issue/ENG-1"},
		{"fix `https://linear.app/acme/issue/ENG-1`", "https://linear.app/acme/issue/ENG-1"},
		{"**https://linear.app/acme/issue/ENG-1**", "https://linear.app/acme/issue/ENG-1"},
		{"no link, just ENG-1", ""},
		{"", ""},
	} {
		if got := FindIssueURL(tc.text); got != tc.want {
			t.Errorf("FindIssueURL(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
