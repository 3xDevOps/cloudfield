package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

// retiringCaveat ends every text report: what the edge cannot see.
const retiringCaveat = `An empty list is not proof that nobody is left: the edge records only while an
option that retires a pair is set; a server that has not enrolled or been
claimed since, and a device token or session not used since, have no origin;
an older image run in between records nothing; and an unclaimed server has no
row at all (aether_edge_servers_by_origin counts those connected now).
Connector counts include requests that were refused or carried no valid token.
`

// retiring prints who the edge last saw on each origin pair it recorded,
// or on the pair of one relay origin. It reads the database through a
// read-only connection, so it neither migrates nor writes.
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
	db, err := edgeDB(*dataDir)
	if err != nil {
		return err
	}
	unlock, err := lockDB(*dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	now := time.Now().UTC()
	rep, err := edgestore.ReadRetiring(context.Background(), db, now)
	if errors.Is(err, edgestore.ErrNoOrigins) {
		_, err = fmt.Fprintf(out, "nothing is recorded yet: %v, and an edge records origins from schema %d on\n", err, edgestore.OriginsSchema)
		return err
	}
	if err != nil {
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
			fmt.Fprintln(w, "no server last enrolled or claimed here") //nolint:errcheck // Flush reports it
		} else {
			fmt.Fprintln(w, "SERVER ID\tNAME\tOWNER LOGIN\tBUILD\tLAST ENROLLED OR CLAIMED") //nolint:errcheck // Flush reports it
			for _, e := range r.Servers {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, printable(e.Name), printable(e.OwnerLogin), //nolint:errcheck // Flush reports it
					printable(e.AgentVersion), e.LastSeenAt.Format(time.RFC3339))
			}
		}
		fmt.Fprintln(w) //nolint:errcheck // Flush reports it
		if r.SigninCountedUnder != "" {
			fmt.Fprintf(w, "device tokens and sessions of %s are counted under relay origin %s, which the edge pairs with it more recently\n\n", //nolint:errcheck // Flush reports it
				r.SigninOrigin, r.SigninCountedUnder)
		}
		printCounts(w, append(signinRows(r.SigninCounts, " here"),
			namedCounts{"servers with connector requests through here", r.ConnectorServers}))
	}
	for _, u := range rep.Unpaired {
		fmt.Fprintf(w, "\nrecorded at an address this edge no longer pairs: %s\n\n", u.SigninOrigin) //nolint:errcheck // Flush reports it
		printCounts(w, signinRows(u.SigninCounts, " there"))
	}
	fmt.Fprintf(w, "\nunknown origin: %d claimed servers not enrolled or claimed since recording began\n\n", rep.Unknown.Servers) //nolint:errcheck // Flush reports it
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

func signinRows(c edgestore.SigninCounts, where string) []namedCounts {
	return []namedCounts{
		{"device tokens issued" + where, c.DeviceTokens},
		{"device tokens issued earlier, first seen" + where, c.DeviceTokensSeen},
		{"accounts with a session" + where, c.SessionAccounts},
		{"accounts with device tokens" + where + " and none recorded at another origin", c.DeviceOnlyAccounts},
	}
}

func printCounts(w io.Writer, rows []namedCounts) {
	fmt.Fprintln(w, "LAST USED WITHIN\t1 DAY\t7 DAYS\t30 DAYS\tTOTAL") //nolint:errcheck // the tabwriter's Flush reports it
	for _, c := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", c.label, c.n.Day, c.n.Week, c.n.Month, c.n.Total) //nolint:errcheck // the tabwriter's Flush reports it
	}
}

// printable is s for a terminal: "-" when empty, quoted with escapes when
// it holds a rune that does not print, such as a bidi control a server
// may put in its name.
func printable(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return dash(s)
}
