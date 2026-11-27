package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestPrintConnectors(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	const universal, own = "https://edge.onaether.dev/mcp", "https://edge.onaether.dev/mcp/abc"
	code := &protocol.ConnectorCodeResult{URL: own, Code: "ABCD-EFGH", ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	connectors := []protocol.Connector{
		{ID: "con-1", ClientName: "ChatGPT", RedirectHost: "chatgpt.com", Resource: own, Scopes: []string{"read", "write"},
			CreatedAt: now.UTC().Format(time.RFC3339), LastUsedAt: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
		{ID: "con-2", ClientName: "Claude", RedirectHost: "claude.ai", Resource: universal, Scopes: []string{"read", "write", "control"},
			CreatedAt: now.UTC().Format(time.RFC3339)},
	}
	const otherUniversal = "https://edge.example.test/mcp"
	other := protocol.Connector{ID: "con-3", ClientName: "ChatGPT", RedirectHost: "chatgpt.com", Resource: otherUniversal, Scopes: []string{"read"},
		CreatedAt: now.UTC().Format(time.RFC3339)}
	for _, tt := range []struct {
		name string
		list protocol.ConnectorListResult
		code *protocol.ConnectorCodeResult
		want string
	}{
		{"older server", protocol.ConnectorListResult{Connectors: connectors[:1]}, code, `Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect, if it asks
Connected:
  con-1  ChatGPT (chatgpt.com)  scopes read,write  added 2026-10-18  last used 2h ago  at https://edge.onaether.dev/mcp/abc
Remove one: aether mcp revoke <id>
`},
		{"no GitHub account", protocol.ConnectorListResult{URL: own, UniversalURL: universal}, code, `Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect, if it asks
To use the one URL https://edge.onaether.dev/mcp instead, link a GitHub account: aether member link --github <login>
`},
		{"signing in replaces the code", protocol.ConnectorListResult{Connectors: connectors, URL: universal, UniversalURL: universal, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://edge.onaether.dev/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  ChatGPT (chatgpt.com)  scopes read,write  added 2026-10-18  last used 2h ago  at https://edge.onaether.dev/mcp/abc
  con-2  Claude (claude.ai)  scopes read,write,control  added 2026-10-18  never used  universal URL
Move con-1 to the universal URL: connect again at https://edge.onaether.dev/mcp, then aether mcp revoke <id>
Reconnecting one at the server's own URL asks for a code: aether mcp code
Remove one: aether mcp revoke <id>
`},
		{"at the edge's other relay address", protocol.ConnectorListResult{Connectors: []protocol.Connector{connectors[1], other}, URL: universal,
			UniversalURL: universal, UniversalURLs: []string{universal, otherUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://edge.onaether.dev/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-2  Claude (claude.ai)  scopes read,write,control  added 2026-10-18  never used  universal URL
  con-3  ChatGPT (chatgpt.com)  scopes read  added 2026-10-18  never used  universal URL
Remove one: aether mcp revoke <id>
`},
	} {
		var out strings.Builder
		if err := printConnectors(&out, tt.list, tt.code, now); err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want {
			t.Errorf("%s: output:\n%s\nwant:\n%s", tt.name, out.String(), tt.want)
		}
	}
}

func TestPrintConnectorsAtRetiringAddress(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	const (
		oldUniversal, oldOwn = "https://relay-old.example.test/mcp", "https://relay-old.example.test/mcp/abc"
		newUniversal, newOwn = "https://relay-new.example.test/mcp", "https://relay-new.example.test/mcp/abc"
	)
	added := now.UTC().Format(time.RFC3339)
	at := func(id, resource string, notice *protocol.ConnectorNotice) protocol.Connector {
		return protocol.Connector{ID: id, ClientName: "Claude", RedirectHost: "claude.ai", Resource: resource, Scopes: []string{"read"},
			CreatedAt: added, Notice: notice}
	}
	code := &protocol.ConnectorCodeResult{URL: newOwn, Code: "ABCD-EFGH", ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	for _, tt := range []struct {
		name string
		list protocol.ConnectorListResult
		code *protocol.ConnectorCodeResult
		want string
	}{
		{"no notice", protocol.ConnectorListResult{Connectors: []protocol.Connector{at("con-1", newUniversal, nil)}, URL: newUniversal,
			UniversalURL: newUniversal, UniversalURLs: []string{oldUniversal, newUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://relay-new.example.test/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  Claude (claude.ai)  scopes read  added 2026-11-26  never used  universal URL
Remove one: aether mcp revoke <id>
`},
		{"enrolled at the retiring address", protocol.ConnectorListResult{Connectors: []protocol.Connector{
			at("con-1", oldUniversal, &protocol.ConnectorNotice{State: "retiring", URL: newUniversal, Date: "2027-03-01"}),
			at("con-2", oldOwn, &protocol.ConnectorNotice{State: "retiring", URL: newOwn, Date: "2027-03-01"}),
			at("con-3", newUniversal, nil),
		}, URL: newUniversal, UniversalURL: newUniversal, UniversalURLs: []string{oldUniversal, newUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://relay-new.example.test/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  Claude (claude.ai)  scopes read  added 2026-11-26  never used  universal URL
    retiring on 2027-03-01: add it again at https://relay-new.example.test/mcp, check it works, remove the old one in the app, then: aether mcp revoke con-1
  con-2  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp/abc
    retiring on 2027-03-01: add it again at https://relay-new.example.test/mcp/abc, check it works, remove the old one in the app, then: aether mcp revoke con-2
  con-3  Claude (claude.ai)  scopes read  added 2026-11-26  never used  universal URL
Reconnecting one at the server's own URL asks for a code: aether mcp code
Remove one: aether mcp revoke <id>
`},
		{"retiring without a date, own URL", protocol.ConnectorListResult{Connectors: []protocol.Connector{
			at("con-2", oldOwn, &protocol.ConnectorNotice{State: "retiring", URL: newOwn}),
		}, URL: newOwn}, code,
			`Add this server to ChatGPT or Claude as a custom connector:
  URL   https://relay-new.example.test/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect, if it asks
Connected:
  con-2  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp/abc
    retiring: add it again at https://relay-new.example.test/mcp/abc, check it works, remove the old one in the app, then: aether mcp revoke con-2
Remove one: aether mcp revoke <id>
`},
		{"enrolled at the replacement of a retired address", protocol.ConnectorListResult{Connectors: []protocol.Connector{
			at("con-1", oldUniversal, &protocol.ConnectorNotice{State: "retired", URL: newUniversal}),
			at("con-2", oldOwn, &protocol.ConnectorNotice{State: "retired"}),
		}, URL: newUniversal, UniversalURL: newUniversal, UniversalURLs: []string{newUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://relay-new.example.test/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp
    retired: add it again at https://relay-new.example.test/mcp if you still use it, remove the old one in the app, then: aether mcp revoke con-1
  con-2  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp/abc
    retired: remove it in the app, then: aether mcp revoke con-2
Remove one: aether mcp revoke <id>
`},
		{"no URL the server takes yet", protocol.ConnectorListResult{Connectors: []protocol.Connector{
			at("con-1", oldUniversal, &protocol.ConnectorNotice{State: "retiring", Date: "2027-03-01"}),
		}, URL: oldUniversal, UniversalURL: oldUniversal, UniversalURLs: []string{oldUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://relay-old.example.test/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  Claude (claude.ai)  scopes read  added 2026-11-26  never used  universal URL
    retiring on 2027-03-01: ask this server's admin where to add it again
Remove one: aether mcp revoke <id>
`},
		{"own URL at a retiring address, signing in elsewhere", protocol.ConnectorListResult{Connectors: []protocol.Connector{
			at("con-2", oldOwn, &protocol.ConnectorNotice{State: "retiring", URL: newOwn}),
		}, URL: newUniversal, UniversalURL: newUniversal, UniversalURLs: []string{oldUniversal, newUniversal}, SignIn: true}, nil,
			`Install Cloudfield from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://relay-new.example.test/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-2  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp/abc
    retiring: add it again at https://relay-new.example.test/mcp/abc, check it works, remove the old one in the app, then: aether mcp revoke con-2
Reconnecting one at the server's own URL asks for a code: aether mcp code
Remove one: aether mcp revoke <id>
`},
	} {
		var out strings.Builder
		if err := printConnectors(&out, tt.list, tt.code, now); err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want {
			t.Errorf("%s: output:\n%s\nwant:\n%s", tt.name, out.String(), tt.want)
		}
	}
}

func TestMCPOverviewWithoutACode(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	list := protocol.ConnectorListResult{URL: "https://relay-old.example.test/mcp/abc", Connectors: []protocol.Connector{{ID: "con-1", ClientName: "Claude",
		RedirectHost: "claude.ai", Resource: "https://relay-old.example.test/mcp/abc", Scopes: []string{"read"}, CreatedAt: now.UTC().Format(time.RFC3339),
		Notice: &protocol.ConnectorNotice{State: "retiring", Date: "2027-03-01"}}}}
	refused := &protocol.Error{Code: protocol.CodeUnavailable, Message: "connector.code: this server is not connected to its edge at https://relay-old.example.test"}
	call := func(method string, _, result any) error {
		if method == protocol.MethodConnectorList {
			*result.(*protocol.ConnectorListResult) = list
			return nil
		}
		return refused
	}
	var out strings.Builder
	if err := mcpOverview(&out, call, now); err == nil || err.Error() != refused.Error() {
		t.Fatalf("error %v, want %v", err, refused)
	}
	unmarked := list
	unmarked.Connectors = []protocol.Connector{list.Connectors[0]}
	unmarked.Connectors[0].Notice = nil
	var none strings.Builder
	if err := mcpOverview(&none, func(method string, _, result any) error {
		if method == protocol.MethodConnectorList {
			*result.(*protocol.ConnectorListResult) = unmarked
			return nil
		}
		return refused
	}, now); err == nil || err.Error() != refused.Error() || none.Len() != 0 {
		t.Fatalf("with no notice: error %v, output %q, want %v and no output", err, none.String(), refused)
	}
	if want := `Connected:
  con-1  Claude (claude.ai)  scopes read  added 2026-11-26  never used  at https://relay-old.example.test/mcp/abc
    retiring on 2027-03-01: ask this server's admin where to add it again
Remove one: aether mcp revoke <id>
`; out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
