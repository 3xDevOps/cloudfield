package cli

import (
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
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

// Known reports whether this machine has its own reason to trust origin
// as an edge address: it holds a token there, a saved link names it, or
// it is the client's built-in default edge. A notice that names an
// address no such reason covers is only the old address's word.
func (h EdgeHoldings) Known(origin string) bool {
	if h.SignedInAt(origin) || SameEdge(origin, edgeclient.DefaultURL) || SameEdge(h.Config.EdgeURL, origin) {
		return true
	}
	for _, l := range h.Config.Links {
		if SameEdge(l.EdgeURL, origin) {
			return true
		}
	}
	return false
}

// SameEdge reports whether a and b name the same relay origin.
func SameEdge(a, b string) bool {
	ca, errA := edgeclient.Canonical(a)
	cb, errB := edgeclient.Canonical(b)
	return errA == nil && errB == nil && ca == cb
}
