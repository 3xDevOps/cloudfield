package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

const devicesUsage = `usage: aether-edge devices end --no-address --unused-since <YYYY-MM-DD> [--data <dir>]

commands:
  end    delete the device tokens with no recorded sign-in origin last used before the date, as signing out does
`

// retiring prints who is left on each origin pair the edge has served, or
// on the pair of one relay origin.
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
	now := time.Now()
	reports, err := s.OriginReports(context.Background(), now)
	if err = errors.Join(err, s.Close()); err != nil {
		return err
	}
	if want != "" {
		var known []string
		var match []edgestore.OriginReport
		for _, r := range reports {
			known = append(known, r.RelayOrigin)
			if r.RelayOrigin == want {
				match = append(match, r)
			}
		}
		if len(match) == 0 {
			return fmt.Errorf("%s is not a relay origin this edge has served; it has served: %s", want, dash(strings.Join(known, ", ")))
		}
		reports = match
	}
	if reports == nil {
		reports = []edgestore.OriginReport{}
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			At      time.Time                `json:"at"`
			Origins []edgestore.OriginReport `json:"origins"`
		}{now.UTC(), reports})
	}
	if len(reports) == 0 {
		_, err := fmt.Fprintln(out, "no origin pairs recorded: the edge records the pairs it serves when it starts")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, r := range reports {
		if i > 0 {
			fmt.Fprintln(w) //nolint:errcheck // Flush reports it
		}
		fmt.Fprintf(w, "relay origin %s, sign-in origin %s\n\n", r.RelayOrigin, r.SigninOrigin) //nolint:errcheck // Flush reports it
		if len(r.Servers) == 0 {
			fmt.Fprintln(w, "no server last enrolled here") //nolint:errcheck // Flush reports it
		} else {
			fmt.Fprintln(w, "SERVER ID\tNAME\tOWNER LOGIN\tBUILD\tLAST ENROLLED") //nolint:errcheck // Flush reports it
			for _, e := range r.Servers {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, dash(e.Name), dash(e.OwnerLogin), //nolint:errcheck // Flush reports it
					dash(e.AgentVersion), e.EnrolledAt.Format(time.RFC3339))
			}
		}
		fmt.Fprintln(w)                                                    //nolint:errcheck // Flush reports it
		fmt.Fprintln(w, "LAST USED WITHIN\t1 DAY\t7 DAYS\t30 DAYS\tTOTAL") //nolint:errcheck // Flush reports it
		for _, c := range []struct {
			label string
			n     edgestore.Counts
		}{
			{"device tokens issued here", r.DeviceTokens},
			{"accounts with a session issued here", r.SessionAccounts},
			{"accounts with devices here and none at another origin", r.DeviceOnlyAccounts},
			{"servers with connector requests through here", r.ConnectorServers},
		} {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", c.label, c.n.Day, c.n.Week, c.n.Month, c.n.Total) //nolint:errcheck // Flush reports it
		}
	}
	return w.Flush()
}

// devices runs aether-edge devices end, which reads the confirmation from
// in.
func devices(args []string, getenv func(string) string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || args[0] != "end" {
		if len(args) > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "aether-edge: unknown command %q\n", "devices "+args[0])
		}
		_, _ = fmt.Fprint(os.Stderr, devicesUsage)
		return errUsage
	}
	fs := flag.NewFlagSet("aether-edge devices end", flag.ContinueOnError)
	dataDir := fs.String("data", dataDirDefault(getenv), "data directory of the edge")
	noAddress := fs.Bool("no-address", false, "end only device tokens with no recorded sign-in origin (required)")
	since := fs.String("unused-since", "", "end only device tokens last used before this UTC date, YYYY-MM-DD (required)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("aether-edge devices end takes no arguments after its flags, got %d", fs.NArg())
	}
	if !*noAddress {
		return errors.New("aether-edge devices end ends only device tokens with no recorded sign-in origin: pass --no-address")
	}
	before, err := time.Parse(time.DateOnly, *since)
	if err != nil {
		return fmt.Errorf("--unused-since %q is not a YYYY-MM-DD date: %w", *since, err)
	}
	s, unlock, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	return errors.Join(endDevices(context.Background(), s, before, in, out), s.Close())
}

func endDevices(ctx context.Context, s *edgestore.Store, before time.Time, in io.Reader, out io.Writer) error {
	list, err := s.UnaddressedDevices(ctx, before)
	if err != nil {
		return err
	}
	date := before.Format(time.DateOnly)
	if len(list) == 0 {
		_, err := fmt.Fprintf(out, "no device token without a sign-in origin was last used before %s\n", date)
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE ID\tACCOUNT\tLOGIN OR EMAIL\tLABEL\tLAST USED") //nolint:errcheck // Flush reports it
	ids := make([]string, 0, len(list))
	for _, d := range list {
		ids = append(ids, d.ID)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, accountKey(d.Account), dash(contact(d.Account)), //nolint:errcheck // Flush reports it
			dash(d.Label), d.LastUsedAt.Format(time.RFC3339))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "end these %d device tokens, as signing out does? Type yes to end them: ", len(list)); err != nil {
		return err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the answer: %w", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		_, err := fmt.Fprintln(out, "\nended nothing")
		return err
	}
	ended, err := s.EndUnaddressedDevices(ctx, ids, before)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "ended %d device tokens; %d used since the list was read stay\n", len(ended), len(ids)-len(ended))
	return err
}
