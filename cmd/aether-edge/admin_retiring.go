package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

// retiringCaveat ends every text report: what the edge cannot see.
const retiringCaveat = `An empty list is not proof that nobody is left: the edge records only while an
option that retires a pair is set, a server that has not enrolled since then
and a device token or session not used since then have no origin, and an
unclaimed server has no row at all (aether_edge_servers_by_origin counts those
connected now).
`

// retiring prints who the edge last saw on each origin pair it recorded,
// or on the pair of one relay origin.
func retiring(args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("aether-edge retiring", flag.ContinueOnError)
	dataDir := fs.String("data", dataDirDefault(getenv), "data directory of the edge")
	origin := fs.String("origin", "", "report only the origin pair of this relay origin")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("aether-edge retiring takes no arguments after its flags, got %d", fs.NArg())
	}
	want := ""
	if *origin != "" {
		o, err := edgeproto.Origin(*origin)
		if err != nil {
			return fmt.Errorf("--origin: %w", err)
		}
		want = o
	}
	s, unlock, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	now := time.Now().UTC()
	rep, err := s.Retiring(context.Background(), now)
	if err = errors.Join(err, s.Close()); err != nil {
		return err
	}
	if want != "" {
		var known []string
		var match []edgestore.OriginReport
		for _, r := range rep.Origins {
			known = append(known, r.RelayOrigin)
			if r.RelayOrigin == want {
				match = append(match, r)
			}
		}
		if len(match) == 0 {
			return fmt.Errorf("%s is not a relay origin this edge has recorded; it has recorded: %s", want, dash(strings.Join(known, ", ")))
		}
		rep.Origins = match
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			At time.Time `json:"at"`
			edgestore.Report
		}{now, rep})
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if rep.RecordingSince.IsZero() {
		fmt.Fprintln(w, "no origin pairs recorded: the edge records them from its first start with an option that retires a pair") //nolint:errcheck // Flush reports it
	} else {
		fmt.Fprintf(w, "recording since %s\n", rep.RecordingSince.Format(time.RFC3339)) //nolint:errcheck // Flush reports it
	}
	for _, r := range rep.Origins {
		fmt.Fprintf(w, "\nrelay origin %s, sign-in origin %s\n\n", r.RelayOrigin, r.SigninOrigin) //nolint:errcheck // Flush reports it
		if len(r.Servers) == 0 {
			fmt.Fprintln(w, "no server last seen here") //nolint:errcheck // Flush reports it
		} else {
			fmt.Fprintln(w, "SERVER ID\tNAME\tOWNER LOGIN\tBUILD\tLAST SEEN") //nolint:errcheck // Flush reports it
			for _, e := range r.Servers {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, dash(e.Name), dash(e.OwnerLogin), //nolint:errcheck // Flush reports it
					dash(e.AgentVersion), e.LastSeenAt.Format(time.RFC3339))
			}
		}
		fmt.Fprintln(w) //nolint:errcheck // Flush reports it
		printCounts(w, []namedCounts{
			{"device tokens issued here", r.DeviceTokens},
			{"device tokens issued earlier, first seen here", r.DeviceTokensSeen},
			{"accounts with a session here", r.SessionAccounts},
			{"accounts with device tokens here and none at another origin", r.DeviceOnlyAccounts},
			{"servers with connector requests through here", r.ConnectorServers},
		})
	}
	fmt.Fprintf(w, "\nunknown origin: %d claimed servers not seen since recording began\n\n", rep.Unknown.Servers) //nolint:errcheck // Flush reports it
	printCounts(w, []namedCounts{
		{"device tokens", rep.Unknown.DeviceTokens},
		{"accounts with a session", rep.Unknown.SessionAccounts},
	})
	fmt.Fprint(w, "\n"+retiringCaveat) //nolint:errcheck // Flush reports it
	return w.Flush()
}

type namedCounts struct {
	label string
	n     edgestore.Counts
}

func printCounts(w io.Writer, rows []namedCounts) {
	fmt.Fprintln(w, "LAST USED WITHIN\t1 DAY\t7 DAYS\t30 DAYS\tTOTAL") //nolint:errcheck // the tabwriter's Flush reports it
	for _, c := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", c.label, c.n.Day, c.n.Week, c.n.Month, c.n.Total) //nolint:errcheck // the tabwriter's Flush reports it
	}
}
