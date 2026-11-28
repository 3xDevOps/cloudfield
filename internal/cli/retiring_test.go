package cli

import (
	"testing"

	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
)

func TestEdgeHoldingsKnown(t *testing.T) {
	const (
		old  = "https://old.example.test"
		next = "https://new.example.test"
	)
	for _, tc := range []struct {
		name        string
		h           EdgeHoldings
		old, target string
		want        bool
	}{
		{"nothing", EdgeHoldings{SignedIn: []string{old}}, old, next, false},
		{"a token there, another spelling", EdgeHoldings{SignedIn: []string{next + ":443"}}, old, next, true},
		{"the default link", EdgeHoldings{Config: Config{EdgeURL: next}}, old, next, true},
		{"a named link", EdgeHoldings{Config: Config{Links: []NamedLink{{Name: "box", EdgeURL: next}}}}, old, next, true},
		{"the built-in default named by a self-hosted edge", EdgeHoldings{}, old, edgeclient.DefaultURL, false},
		{"the built-in default named by a hosted address", EdgeHoldings{}, edgeclient.FormerURL, edgeclient.DefaultURL, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.h.Known(tc.old, tc.target); got != tc.want {
				t.Errorf("Known(%s, %s) = %v, want %v", tc.old, tc.target, got, tc.want)
			}
		})
	}
}
