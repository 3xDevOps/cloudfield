package connector

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	edgeagent "github.com/3xDevOps/cloudfield/internal/edge/agent"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const (
	newRelay = "https://relay-new.example.test"
	oldRelay = "https://relay-old.example.test"
)

func stored(firstUse bool, notices ...edgeproto.Notice) edgeagent.StoredNotices {
	return edgeagent.StoredNotices{Received: time.Now(), FirstUse: firstUse, Notices: notices}
}

// authorizeURL is a valid authorization request for the server's own
// connector URL.
func (e *env) authorizeURL() string {
	q := url.Values{"response_type": {"code"}, "client_id": {e.clientID}, "redirect_uri": {e.redirect}, "state": {"s"},
		"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}, "resource": {e.base}}
	return e.base + "/authorize?" + q.Encode()
}

func TestConsentPageAtRetiringAddress(t *testing.T) {
	e := newEnv(t)
	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	path := strings.TrimPrefix(e.base, e.origin)
	both := []string{e.origin, newRelay}
	for _, tt := range []struct {
		name    string
		relays  []string
		notices edgeagent.StoredNotices
		want    string
	}{
		{"no notice", both, edgeagent.StoredNotices{}, ""},
		{"retiring with a date", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"}),
			`<p class="retiring">This address is retiring on <time>2027-03-01</time>. Add the connector at <code>` + newRelay + path + `</code> instead. Connecting here still works until it is retired.</p>`},
		{"retiring without a date", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}),
			`<p class="retiring">This address is retiring. Add the connector at <code>` + newRelay + path + `</code> instead. Connecting here still works until it is retired.</p>`},
		{"first use", both, stored(true, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}),
			`<p class="retiring">This address is retiring. Add the connector at <code>` + newRelay + path + `</code> instead. Connecting here still works until it is retired.</p>`},
		{"replacement not announced", nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"}),
			`<p class="retiring">This address is retiring on <time>2027-03-01</time>. Ask this server's admin where to add the connector instead. Connecting here still works until it is retired.</p>`},
		{"another address retiring", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: oldRelay, Replacement: e.origin}), ""},
		{"another address retired", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: oldRelay, Replacement: e.origin}), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.edge.set(tt.relays, tt.notices)
			resp, err := http.Get(e.authorizeURL())
			if err != nil {
				t.Fatal(err)
			}
			page, err := readBody(resp)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("consent page = %d %s", resp.StatusCode, page)
			}
			if tt.want == "" {
				if strings.Contains(page, "retiring") || strings.Contains(page, "time{") {
					t.Fatalf("consent page warns with no notice for its address:\n%s", page)
				}
				return
			}
			if !strings.Contains(page, tt.want) {
				t.Fatalf("consent page lacks %s:\n%s", tt.want, page)
			}
			m := requestField.FindStringSubmatch(page)
			if m == nil {
				t.Fatalf("consent page has no form:\n%s", page)
			}
			resp, err = http.PostForm(e.authorizeURL(), url.Values{"request": {m[1]}, "code": {"zzzz-zzzz"}})
			if err != nil {
				t.Fatal(err)
			}
			if page, err = readBody(resp); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, tt.want) {
				t.Fatalf("wrong code = %d, want 403 still warning %s:\n%s", resp.StatusCode, tt.want, page)
			}
		})
	}
}

func TestConnectorListAtRetiringAddress(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	member, err := e.db.GetMemberByIdentity(ctx, "github", "1")
	if err != nil {
		t.Fatal(err)
	}
	ownPath := strings.TrimPrefix(e.base, e.origin)
	now := time.Now()
	add := func(resource string) string {
		c := &store.Connector{Member: member.ID, ClientID: e.clientID, ClientName: "Test client", RedirectHost: e.clientHost,
			Resource: resource, Scopes: []string{"read"}, CreatedAt: now}
		if err := e.db.CreateConnector(ctx, c, store.ConnectorTokens{Access: tokenHash(resource + "a"), AccessExpiresAt: now.Add(time.Hour),
			Refresh: tokenHash(resource + "r"), RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	resources := []string{e.base, e.origin + edgeproto.PathUniversalMCP, newRelay + ownPath, oldRelay + ownPath, oldRelay + edgeproto.PathUniversalMCP}
	ids := map[string]string{}
	for _, r := range resources {
		ids[add(r)] = r
	}
	retiring := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"}
	undated := retiring
	undated.Date = ""
	retired := edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: oldRelay, Replacement: e.origin}
	both := []string{e.origin, newRelay}
	type want struct {
		url, universal string
		notices        map[string]*protocol.ConnectorNotice
	}
	at := func(state, date, ownURL, universalURL string) map[string]*protocol.ConnectorNotice {
		return map[string]*protocol.ConnectorNotice{
			e.base:            {State: state, URL: ownURL, Date: date},
			e.origin + "/mcp": {State: state, URL: universalURL, Date: date},
		}
	}
	universal := e.origin + "/mcp"
	for _, tt := range []struct {
		name    string
		version int64
		relays  []string
		notices edgeagent.StoredNotices
		err     error
		want    want
	}{
		{"no notice", 2, both, edgeagent.StoredNotices{}, nil, want{url: e.base}},
		{"no notice, universal", 6, both, edgeagent.StoredNotices{}, nil, want{url: universal, universal: universal}},
		{"unreadable notices", 6, both, edgeagent.StoredNotices{}, errors.New("read notices.json: permission denied"), want{url: universal, universal: universal}},
		{"enrolled at a retiring address", 6, both, stored(false, retiring), nil, want{url: newRelay + "/mcp", universal: newRelay + "/mcp",
			notices: at("retiring", "2027-03-01", newRelay+ownPath, newRelay+"/mcp")}},
		{"retiring without a date", 6, both, stored(false, undated), nil, want{url: newRelay + "/mcp", universal: newRelay + "/mcp",
			notices: at("retiring", "", newRelay+ownPath, newRelay+"/mcp")}},
		{"first use", 6, both, stored(true, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", newRelay+ownPath, newRelay+"/mcp")}},
		{"replacement not announced", 6, nil, stored(false, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"edge before relay origins", 3, both, stored(false, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"edge offline", 0, both, stored(false, retiring), nil, want{url: e.base,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"enrolled at the replacement of a retired address", 6, nil, stored(false, retired), nil, want{url: universal, universal: universal,
			notices: map[string]*protocol.ConnectorNotice{
				oldRelay + ownPath: {State: "retired", URL: e.origin + ownPath},
				oldRelay + "/mcp":  {State: "retired", URL: e.origin + "/mcp"},
			}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.edge.version.Store(tt.version)
			e.edge.set(tt.relays, tt.notices)
			e.edge.setErr(tt.err)
			var list protocol.ConnectorListResult
			if err := e.device.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
				t.Fatal(err)
			}
			if list.URL != tt.want.url || list.UniversalURL != tt.want.universal {
				t.Errorf("url %q universal_url %q, want %q %q", list.URL, list.UniversalURL, tt.want.url, tt.want.universal)
			}
			if len(list.Connectors) != len(resources) {
				t.Fatalf("connectors = %+v", list.Connectors)
			}
			for _, c := range list.Connectors {
				got, _ := json.Marshal(c.Notice)
				wantJSON, _ := json.Marshal(tt.want.notices[ids[c.ID]])
				if string(got) != string(wantJSON) {
					t.Errorf("connector at %s: notice %s, want %s", ids[c.ID], got, wantJSON)
				}
			}
			if tt.version == 0 {
				return
			}
			var code protocol.ConnectorCodeResult
			if err := e.device.Call(protocol.MethodConnectorCode, struct{}{}, &code); err != nil {
				t.Fatal(err)
			}
			if code.URL != tt.want.url {
				t.Errorf("connector.code url %q, want %q", code.URL, tt.want.url)
			}
		})
	}
}

func TestToolResultNoticeAtRetiringAddress(t *testing.T) {
	e := newEnv(t)
	c := e.connect(t)
	session, refresh := c.session, c.token.RefreshToken
	// later moves the clock to at and connects again with a refreshed
	// token, since the access token lasts an hour.
	later := func(at time.Duration) {
		t.Helper()
		e.skew.Store(int64(at))
		next := e.refresh(t, c, refresh, http.StatusOK)
		session, refresh = e.session(t, next.AccessToken), next.RefreshToken
	}
	call := func() *mcp.CallToolResult {
		t.Helper()
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_list_workspaces"})
		if err != nil || res.IsError {
			t.Fatalf("aether_list_workspaces = %+v %v", res, err)
		}
		return res
	}
	plain := call()
	if len(plain.Content) != 1 {
		t.Fatalf("with no notice the result has %d content blocks, want 1", len(plain.Content))
	}
	first, _ := json.Marshal(plain.Content[0])
	structured, _ := json.Marshal(plain.StructuredContent)
	same := func(res *mcp.CallToolResult) {
		t.Helper()
		gotFirst, _ := json.Marshal(res.Content[0])
		gotStructured, _ := json.Marshal(res.StructuredContent)
		if string(gotFirst) != string(first) || string(gotStructured) != string(structured) {
			t.Fatalf("first block %s structured %s, want %s %s", gotFirst, gotStructured, first, structured)
		}
	}
	blocks := func(want int) *mcp.CallToolResult {
		t.Helper()
		res := call()
		if len(res.Content) != want {
			t.Fatalf("result has %d content blocks, want %d: %+v", len(res.Content), want, res.Content)
		}
		same(res)
		return res
	}
	notice := func(res *mcp.CallToolResult) string {
		t.Helper()
		text, ok := res.Content[1].(*mcp.TextContent)
		if !ok {
			t.Fatalf("second block is %T, want text", res.Content[1])
		}
		return text.Text
	}

	e.edge.set(nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: oldRelay, Replacement: e.origin, Date: "2027-03-01"}))
	blocks(1)
	e.edge.set(nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: e.origin, Replacement: newRelay}))
	blocks(1)

	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	retiring := stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"})
	e.edge.set(nil, retiring)
	blocks(1)

	both := []string{e.origin, newRelay}
	e.edge.set(both, retiring)
	for _, name := range []string{"search"} {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError || len(res.Content) != 1 {
			t.Fatalf("%s result = error %v, %d content blocks %+v, want one", name, res.IsError, len(res.Content), res.Content)
		}
	}
	newURL := newRelay + strings.TrimPrefix(e.base, e.origin)
	if got, want := notice(blocks(2)), "Notice for the user: this connector was added at "+e.base+
		", an address that is retiring on 2027-03-01. It must be added again at "+newURL+"."; got != want {
		t.Fatalf("notice %q, want %q", got, want)
	}
	blocks(1)
	later(24*time.Hour - time.Second)
	blocks(1)

	e.edge.set(both, stored(true, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}))
	later(24 * time.Hour)
	if got, want := notice(blocks(2)), "Notice for the user: this connector was added at "+e.base+
		", an address that is retiring. It must be added again at "+newURL+"."; got != want {
		t.Fatalf("notice %q, want %q", got, want)
	}
	blocks(1)
}
