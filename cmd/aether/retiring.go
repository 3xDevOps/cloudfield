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

// printRetiring tells, once per run, of notices, the edges whose answers
// this run said their address is retiring. A notice is the old address's own,
// unauthenticated word, so commands are printed only for a replacement
// this machine already knows, and nothing is changed or signed in.
func printRetiring(w io.Writer, notices map[string]edgeclient.Retiring) {
	if len(notices) == 0 {
		return
	}
	dir, err := cli.Dir()
	var holdings cli.EdgeHoldings
	if err == nil {
		holdings, err = cli.LoadEdgeHoldings(dir)
	}
	var daemon []string
	if err == nil {
		daemon, err = localops.InstalledDaemonArgs()
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "cloudfield: an edge says it is retiring, and what this machine holds there could not be read: %v\n", err)
	}
	for _, origin := range slices.Sorted(maps.Keys(notices)) {
		_, _ = io.WriteString(w, retiringNotice(origin, notices[origin], holdings, daemon, err == nil))
	}
}

// retiringNotice is what printRetiring says of the edge at origin. read
// is whether holdings and daemon, the installed sync daemon's run flags,
// could be read.
func retiringNotice(origin string, r edgeclient.Retiring, holdings cli.EdgeHoldings, daemon []string, read bool) string {
	old, err := edgeclient.Canonical(origin)
	if err != nil || cli.SameEdge(old, r.Replacement) {
		return ""
	}
	when := ""
	if r.Date != "" {
		when = " on " + r.Date
	}
	notice := fmt.Sprintf("cloudfield: %s says it is retiring%s and that its new address is %s.\n", old, when, r.Replacement)
	if !read || !holdings.Known(old, r.Replacement) {
		return notice + "Confirm that address with whoever runs the edge before you sign in there.\n"
	}
	var commands []string
	if !holdings.SignedInAt(r.Replacement) {
		commands = append(commands, "cloudfield login --edge "+r.Replacement)
	}
	cfg := holdings.Config
	if cfg.ServerID != "" && cli.SameEdge(cfg.EdgeURL, old) {
		commands = append(commands, relinkCommand(r.Replacement, "", cfg.Addr, cfg.ServerID))
	}
	for _, l := range cfg.Links {
		if l.ServerID != "" && cli.SameEdge(l.EdgeURL, old) {
			commands = append(commands, relinkCommand(r.Replacement, l.Name, l.Addr, l.ServerID))
		}
	}
	if i := slices.Index(daemon, "--edge-url"); i >= 0 && i+1 < len(daemon) && cli.SameEdge(daemon[i+1], old) {
		args := slices.Clone(daemon)
		args[i+1] = r.Replacement
		for j, a := range args {
			args[j] = shellquote.Quote(a)
		}
		commands = append(commands, "cloudfield daemon install "+strings.Join(args, " "))
	}
	if holdings.SignedInAt(old) {
		commands = append(commands, "cloudfield logout --edge "+old)
	}
	if len(commands) == 0 {
		return notice
	}
	return notice + "Move this machine there with:\n  " + strings.Join(commands, "\n  ") + "\n"
}

func relinkCommand(edge, name, addr, serverID string) string {
	cmd := "cloudfield link --edge " + edge
	if name != "" {
		cmd += " --name " + shellquote.Quote(name)
	}
	if addr != "" {
		cmd += " --addr " + shellquote.Quote(addr)
	}
	return cmd + " " + serverID
}
