package cli

import (
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
)

// EdgeHoldings is what this machine holds at edges: the relay origins it
// holds a device token for and the saved links.
type EdgeHoldings struct {
	SignedIn []string
	Config   Config
}

// LoadEdgeHoldings reads the tokens in dir and the saved links.
func LoadEdgeHoldings(dir string) (EdgeHoldings, error) {
	signedIn, err := edgeclient.SignedIn(dir)
	if err != nil {
		return EdgeHoldings{}, err
	}
	cfg, err := Saved()
	if err != nil {
		return EdgeHoldings{}, err
	}
	return EdgeHoldings{SignedIn: signedIn, Config: cfg}, nil
}

// SignedInAt reports whether h holds a device token for the edge at
// origin.
func (h EdgeHoldings) SignedInAt(origin string) bool {
	for _, o := range h.SignedIn {
		if SameEdge(o, origin) {
			return true
		}
	}
	return false
}

// Known reports whether this machine has its own reason to trust
// replacement, named by the edge at old: it holds a token there or a saved
// link names it. The client's built-in default edge counts only when old
// is itself one of the project's hosted addresses. A notice that names an
// address no such reason covers is only the old address's word.
func (h EdgeHoldings) Known(old, replacement string) bool {
	if h.SignedInAt(replacement) || SameEdge(h.Config.EdgeURL, replacement) {
		return true
	}
	for _, l := range h.Config.Links {
		if SameEdge(l.EdgeURL, replacement) {
			return true
		}
	}
	return edgeclient.Hosted(old) && SameEdge(replacement, edgeclient.DefaultURL)
}

// SameEdge reports whether a and b name the same relay origin.
func SameEdge(a, b string) bool {
	ca, errA := edgeclient.Canonical(a)
	cb, errB := edgeclient.Canonical(b)
	return errA == nil && errB == nil && ca == cb
}
