package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	"github.com/3xDevOps/cloudfield/internal/localops"
	"github.com/3xDevOps/cloudfield/internal/shellquote"
)

const movingGuide = "https://github.com/3xDevOps/cloudfield/blob/main/docs/edge-retire.md#moving-a-device"

// printRetiring tells, once per run, of notices, the edges whose answers
// this run said their address is retiring. A notice is the old address's
// own, unauthenticated word, so it prints a command only to sign in at a
// replacement this machine already knows, and changes nothing.
func printRetiring(w io.Writer, notices map[string]edgeclient.Retiring) {
	notices = maps.Clone(notices)
	maps.DeleteFunc(notices, func(origin string, r edgeclient.Retiring) bool { return cli.SameEdge(origin, r.Replacement) })
	if len(notices) == 0 {
		return
	}
	dir, err := cli.Dir()
	var holdings cli.EdgeHoldings
	if err == nil {
		holdings, err = cli.LoadEdgeHoldings(dir)
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "cloudfield: an edge says it is retiring, and what this machine holds there could not be read: %v\n", err)
	}
	// A unit that cannot be read is left unmentioned.
	daemon, _ := localops.InstalledDaemonArgs()
	daemonEdge := ""
	if i := slices.Index(daemon, "--edge-url"); i >= 0 && i+1 < len(daemon) {
		daemonEdge = daemon[i+1]
	}
	for _, origin := range slices.Sorted(maps.Keys(notices)) {
		_, _ = io.WriteString(w, retiringNotice(origin, notices[origin], holdings, daemonEdge, err == nil))
	}
}

// retiringNotice is what printRetiring says of the edge at origin. read
// is whether holdings could be read; daemonEdge is the --edge-url of the
// installed sync daemon's unit, empty without one.
func retiringNotice(origin string, r edgeclient.Retiring, holdings cli.EdgeHoldings, daemonEdge string, read bool) string {
	old, err := edgeclient.Canonical(origin)
	if err != nil {
		return ""
	}
	when := ""
	if r.Date != "" {
		when = " on " + r.Date
	}
	lines := []string{fmt.Sprintf("cloudfield: %s says it is retiring%s and that its new address is %s.", old, when, r.Replacement)}
	switch {
	case !read || !holdings.Known(old, r.Replacement):
		lines = append(lines, "Confirm that address with whoever runs the edge before you sign in there.")
	case !slices.Contains(holdings.SignedIn, r.Replacement):
		lines = append(lines, "Sign in there with: cloudfield login --edge "+shellquote.Quote(r.Replacement))
	}
	cfg := holdings.Config
	if cfg.ServerID != "" && cli.SameEdge(cfg.EdgeURL, old) {
		lines = append(lines, fmt.Sprintf("The default link (server %s) still names %s.", cfg.ServerID, old))
	}
	for _, l := range cfg.Links {
		if l.ServerID != "" && cli.SameEdge(l.EdgeURL, old) {
			lines = append(lines, fmt.Sprintf("The link %q (server %s) still names %s.", l.Name, l.ServerID, old))
		}
	}
	if daemonEdge != "" && cli.SameEdge(daemonEdge, old) {
		lines = append(lines, fmt.Sprintf("The sync daemon's unit names %s.", old))
	}
	lines = append(lines, "Moving links and the sync daemon, and signing out of "+old+": "+movingGuide)
	return strings.Join(lines, "\n") + "\n"
}
