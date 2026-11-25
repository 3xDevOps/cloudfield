// Package linear recognizes Linear issue links. It holds no credentials and
// makes no network calls.
package linear

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Form is the shape ParseIssueURL accepts, for error messages.
const Form = "https://linear.app/<workspace>/issue/<TEAM>-<number>[/<slug>]"

var (
	identifierPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-([0-9]+)$`)
	slugPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	candidatePattern  = regexp.MustCompile(`https://linear\.app/[^\s<>"']+`)
)

// ParseIssueURL returns the issue identifier, such as ENG-123, and the
// canonical URL https://linear.app/<workspace>/issue/<IDENTIFIER> of a
// Linear issue link. The query and fragment are dropped.
func ParseIssueURL(raw string) (identifier, canonical string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "linear.app" {
		return "", "", fmt.Errorf("linear issue URL %q must have the form %s", raw, Form)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[1] != "issue" || !slugPattern.MatchString(parts[0]) {
		return "", "", fmt.Errorf("linear issue URL %q must have the form %s", raw, Form)
	}
	match := identifierPattern.FindStringSubmatch(parts[2])
	if match == nil {
		return "", "", fmt.Errorf("linear issue URL %q must have the form %s", raw, Form)
	}
	identifier = strings.ToUpper(match[1]) + "-" + match[2]
	return identifier, "https://linear.app/" + parts[0] + "/issue/" + identifier, nil
}

// FindIssueURL returns the canonical URL of the first Linear issue link in
// text, or "" when there is none.
func FindIssueURL(text string) string {
	for _, candidate := range candidatePattern.FindAllString(text, -1) {
		candidate = strings.TrimRight(candidate, ".,;:!?)]}")
		if _, canonical, err := ParseIssueURL(candidate); err == nil {
			return canonical
		}
	}
	return ""
}
