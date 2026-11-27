package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

func init() {
	register(command{
		name:    "logout",
		short:   "revoke this device's edge sign-in, or show how to delete the edge account",
		usage:   "cloudfield logout [--edge <url>] [--delete-account]",
		example: "cloudfield logout",
		flags:   true,
		run:     runLogout,
	})
}

func runLogout(args []string) error {
	fs := newFlagSet("logout")
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	deleteAccount := fs.Bool("delete-account", false, "list what deleting the signed-in account at the edge touches, and the edge page that deletes it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: cloudfield logout [--edge <url>] [--delete-account]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	if *deleteAccount {
		return deleteEdgeAccount(context.Background(), client, os.Stdout)
	}
	unrevoked, err := client.Logout(context.Background())
	if err != nil {
		return err
	}
	dir, err := cli.Dir()
	if err != nil {
		return err
	}
	if unrevoked == nil {
		fmt.Printf("signed out of %s: the device token is revoked at the edge and deleted here; the device key and your links stay\n", client.Host())
		return printStillSignedIn(os.Stdout, dir)
	}
	fmt.Printf("deleted the device token for %s here, but the edge did not confirm the sign-out: it answered that this address is retired and that its new address is %s.\n",
		client.Host(), unrevoked.Replacement)
	fmt.Printf("Device %s may still be signed in; %s.\n", edgeclient.Clean(unrevoked.Device.ID), devicesPage(dir, client.URL(), unrevoked.Replacement))
	return printStillSignedIn(os.Stdout, dir)
}

// devicesPage says where to revoke a device that old, a retired address,
// named replacement as its successor for: the Devices page of the sign-in
// origin this machine recorded for exactly replacement; without such a
// record, of the one it recorded for every other spelling of it; else of
// replacement's when this machine knows it otherwise. An address it does
// not know is only the old address's word, and a record that cannot be
// read, or records that disagree, name no page.
func devicesPage(dir, old, replacement string) string {
	const ask = "ask whoever runs the edge for the address of its Devices page and revoke it there"
	holdings, err := cli.LoadEdgeHoldings(dir)
	if err != nil || !holdings.Known(old, replacement) {
		return ask
	}
	signinAt := func(origin string) (string, bool) {
		c, err := edgeclient.New(dir, origin)
		if err != nil {
			return "", false
		}
		s, err := c.Session()
		return s.SigninOrigin, err == nil
	}
	page := ""
	if slices.Contains(holdings.SignedIn, replacement) {
		signin, ok := signinAt(replacement)
		if !ok {
			return ask
		}
		page = signin
	} else {
		for _, origin := range holdings.SignedIn {
			if !cli.SameEdge(origin, replacement) {
				continue
			}
			signin, ok := signinAt(origin)
			if !ok || (page != "" && signin != page) {
				return ask
			}
			page = signin
		}
	}
	if page == "" {
		return "revoke it on the Devices page of the sign-in origin of " + replacement
	}
	return "revoke it on " + edgeclient.Clean(page) + "/devices"
}

// printStillSignedIn names the edges this machine still holds a token
// for, as after a logout without --edge on a machine signed in to both
// addresses of the project's edge.
func printStillSignedIn(w io.Writer, dir string) error {
	signedIn, err := edgeclient.SignedIn(dir)
	if err != nil {
		return err
	}
	for _, o := range signedIn {
		_, _ = fmt.Fprintf(w, "still signed in to %s; sign out there with: aether logout --edge %s\n", edgeclient.HostOf(o), o)
	}
	return nil
}

// deleteEdgeAccount shows what deleting the signed-in account touches and
// where to delete it. The edge deletes an account only from a browser that
// signed in with GitHub moments before, never with a device token.
func deleteEdgeAccount(ctx context.Context, client *edgeclient.Client, out io.Writer) error {
	sum, err := client.Account(ctx)
	if err != nil {
		return err
	}
	session, err := client.Session()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "account %s at %s\n", accountName(sum.Account.Account), client.Host())
	printServers(out, "servers it owns (each becomes ownerless: transfer first with `aether member transfer <member id>` on that server)", sum.Owned)
	printServers(out, "servers it is a member of", sum.Member)
	_, _ = fmt.Fprintf(out, "deleting it ends its sign-ins and device tokens, and each server above removes this account's identity and\n"+
		"its edge devices. No server loses a member, a role or data: an SSH key or tailnet identity of yours keeps\n"+
		"working there until an admin removes it. An admin gives a server left without an owner an owner again with\n"+
		"Settings › Remote access › Connect my GitHub account in the dashboard, or `aether edge connect`.\n"+
		"delete it in a browser at %s%s: sign in with GitHub there and type %s. Then run `aether logout` here to\n"+
		"delete this machine's token.\n", session.SigninOrigin, edgeproto.PathAccountPage, sum.Confirm)
	return nil
}

func printServers(out io.Writer, title string, servers []edgeproto.ServerInfo) {
	_, _ = fmt.Fprintf(out, "%s:", title)
	if len(servers) == 0 {
		_, _ = fmt.Fprintln(out, " none")
		return
	}
	_, _ = fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, s := range servers {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, s.ID, s.Role)
	}
	_ = tw.Flush()
}
