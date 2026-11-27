package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	register(command{
		name:  "mcp",
		short: "add the linked server to ChatGPT or Claude as a custom connector, or revoke one",
		usage: "cloudfield mcp\n" +
			"cloudfield mcp code\n" +
			"cloudfield mcp revoke <connector-id>",
		example: "cloudfield mcp",
		run:     runMCP,
	})
}

const mcpUsage = "usage: cloudfield mcp [code | revoke <connector id>]"

func runMCP(args []string) error {
	switch {
	case len(args) == 0:
		return withControl(func(c *protocol.Client) error { return mcpOverview(os.Stdout, c.Call, time.Now()) })
	case len(args) == 1 && args[0] == "code":
		return withControl(func(c *protocol.Client) error {
			var code protocol.ConnectorCodeResult
			if err := c.Call(protocol.MethodConnectorCode, struct{}{}, &code); err != nil {
				return olderServer(err)
			}
			return printCode(os.Stdout, code, time.Now())
		})
	case len(args) == 2 && args[0] == "revoke":
		return withControl(func(c *protocol.Client) error {
			var res protocol.ConnectorRevokeResult
			if err := c.Call(protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: args[1]}, &res); err != nil {
				return olderServer(err)
			}
			fmt.Printf("revoked connector %s, %s (%s); its tokens no longer work\n", res.Connector.ID, res.Connector.ClientName, res.Connector.RedirectHost)
			return nil
		})
	}
	return errors.New(mcpUsage)
}

func olderServer(err error) error {
	var rpc *protocol.Error
	if errors.As(err, &rpc) && rpc.Code == protocol.CodeMethodNotFound {
		return fmt.Errorf("%w; the server is older than this CLI, and an admin updates it with aether server update", err)
	}
	return err
}

// mcpOverview prints how to add a connector and the caller's connectors.
// When no code can be minted and the edge says something about the
// address of one of them, it still prints the connectors before the error.
func mcpOverview(w io.Writer, call func(method string, params, result any) error, now time.Time) error {
	var list protocol.ConnectorListResult
	if err := call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
		return olderServer(err)
	}
	var code *protocol.ConnectorCodeResult
	if !list.SignIn {
		code = &protocol.ConnectorCodeResult{}
		if err := call(protocol.MethodConnectorCode, struct{}{}, code); err != nil {
			if !slices.ContainsFunc(list.Connectors, func(c protocol.Connector) bool { return c.Notice != nil }) {
				return olderServer(err)
			}
			if perr := printConnected(w, list, "", false, now); perr != nil {
				return perr
			}
			return olderServer(err)
		}
	}
	return printConnectors(w, list, code, now)
}

func printConnectors(w io.Writer, list protocol.ConnectorListResult, code *protocol.ConnectorCodeResult, now time.Time) error {
	connectorURL := list.URL
	if code != nil {
		connectorURL = code.URL
	}
	if connectorURL != "" && connectorURL == list.UniversalURL {
		_, _ = fmt.Fprintf(w, "Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:\n  URL   %s\n", connectorURL)
	} else {
		_, _ = fmt.Fprintf(w, "Add this server to ChatGPT or Claude as a custom connector:\n  URL   %s\n", connectorURL)
	}
	if code == nil {
		_, _ = fmt.Fprintln(w, "  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow")
	} else if err := printCode(w, *code, now); err != nil {
		return err
	}
	if list.UniversalURL != "" && connectorURL != list.UniversalURL {
		_, _ = fmt.Fprintf(w, "To use the one URL %s instead, link a GitHub account: aether member link --github <login>\n", list.UniversalURL)
	}
	return printConnected(w, list, connectorURL, code == nil, now)
}

// printConnected lists the caller's connectors, connectorURL being the
// URL to add, empty when none was minted, and signIn whether that URL's
// page takes the edge sign-in.
func printConnected(w io.Writer, list protocol.ConnectorListResult, connectorURL string, signIn bool, now time.Time) error {
	if len(list.Connectors) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w, "Connected:")
	var own []string
	ownPath := false
	for _, c := range list.Connectors {
		added, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			return fmt.Errorf("connector %s: created_at: %w", c.ID, err)
		}
		used := "never used"
		if c.LastUsedAt != "" {
			last, err := time.Parse(time.RFC3339, c.LastUsedAt)
			if err != nil {
				return fmt.Errorf("connector %s: last_used_at: %w", c.ID, err)
			}
			used = "last used " + ago(now.Sub(last))
		}
		at := ""
		switch {
		case c.Resource == "":
		case c.Resource == list.UniversalURL || slices.Contains(list.UniversalURLs, c.Resource):
			at = "  universal URL"
		default:
			at = "  at " + c.Resource
			if c.Notice == nil {
				own = append(own, c.ID)
			}
		}
		if c.Notice != nil && c.Notice.URL != "" && !strings.HasSuffix(c.Notice.URL, edgeproto.PathUniversalMCP) {
			ownPath = true
		}
		_, _ = fmt.Fprintf(w, "  %s  %s (%s)  scopes %s  added %s  %s%s\n", c.ID, c.ClientName, c.RedirectHost, strings.Join(c.Scopes, ","),
			added.Local().Format(time.DateOnly), used, at)
		if c.Notice != nil {
			_, _ = fmt.Fprintln(w, "    "+noticeLine(c))
		}
	}
	if len(own) > 0 && connectorURL != "" && connectorURL == list.UniversalURL {
		_, _ = fmt.Fprintf(w, "Move %s to the universal URL: connect again at %s, then aether mcp revoke <id>\n", strings.Join(own, ", "), list.UniversalURL)
	}
	if (len(own) > 0 || ownPath) && signIn {
		_, _ = fmt.Fprintln(w, "Reconnecting one at the server's own URL asks for a code: aether mcp code")
	}
	_, _ = fmt.Fprintln(w, "Remove one: aether mcp revoke <id>")
	return nil
}

// noticeLine is what the edge says about the address connector c was
// added at, and what to do.
func noticeLine(c protocol.Connector) string {
	revoke := "aether mcp revoke " + c.ID
	if c.Notice.State == edgeproto.NoticeRetired {
		if c.Notice.URL == "" {
			return "retired: remove it in the app, then: " + revoke
		}
		return "retired: add it again at " + c.Notice.URL + " if you still use it, remove the old one in the app, then: " + revoke
	}
	on := ""
	if c.Notice.Date != "" {
		on = " on " + c.Notice.Date
	}
	if c.Notice.URL == "" {
		return "retiring" + on + ": ask this server's admin where to add it again"
	}
	return "retiring" + on + ": add it again at " + c.Notice.URL + ", check it works, remove the old one in the app, then: " + revoke
}

func printCode(w io.Writer, code protocol.ConnectorCodeResult, now time.Time) error {
	expires, err := time.Parse(time.RFC3339, code.ExpiresAt)
	if err != nil {
		return fmt.Errorf("connector.code: expires_at: %w", err)
	}
	_, _ = fmt.Fprintf(w, "  Code  %s   one use, expires in %d minutes; paste it on the page that opens when you connect, if it asks\n",
		code.Code, int(expires.Sub(now).Round(time.Minute)/time.Minute))
	return nil
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}
