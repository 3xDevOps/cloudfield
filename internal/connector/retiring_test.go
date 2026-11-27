package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/3xDevOps/cloudfield/internal/domain"
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
			`<p class="retiring">This address is retiring. Ask this server's admin where to add the connector instead. Connecting here still works until it is retired.</p>`},
		{"replacement not announced", nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"}),
			`<p class="retiring">This address is retiring on <time>2027-03-01</time>. Ask this server's admin where to add the connector instead. Connecting here still works until it is retired.</p>`},
		{"another address retiring", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: oldRelay, Replacement: e.origin}), ""},
		{"another address retired", both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: oldRelay, Replacement: e.origin}), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.edge.set(tt.relays, tt.notices)
			e.skew.Add(int64(noticeReadEvery))
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

// mainConsentPages are the SHA-256 hashes of the consent page states with
// no notice as main rendered them before notices, 2e71342e6.
var mainConsentPages = map[string]string{
	"consent-code":       "0782a15763d6398cf5a2eb2c9dcb39d7c3a978e2a96501f6df7235c612ed54b6",
	"consent-code-error": "df2ea017f8c43107f1d9453ec340361a703a98cbdfcff8e17824b96374c4433c",
	"consent-member":     "e54af339ad16f72bf3e15e918a148d92fc4ae33e22f63af76947af0f7f35cd1b",
	"consent-long":       "9fbc3907cd5ed43a06d34e85d75d3b0b373206eddf5ebfbd02281caa48ef5d6b",
	"consent-failed":     "f1be0f7fa0b4563e4e67e8a21151c23b3e28b83d158400984d398dfe43a7c05c",
}

func TestUniversalConsentPageAtRetiringAddress(t *testing.T) {
	e := newEnv(t)
	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	e.edge.set([]string{e.origin, newRelay}, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"}))
	universal := e.origin + edgeproto.PathUniversalMCP
	q := url.Values{"response_type": {"code"}, "client_id": {e.clientID}, "redirect_uri": {e.redirect}, "state": {"s"},
		"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}, "resource": {universal},
		edgeproto.ParamServer: {path.Base(e.base)}}
	resp, err := http.Get(universal + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, err := readBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `<p class="retiring">This address is retiring on <time>2027-03-01</time>. Add the connector at <code>` + newRelay + edgeproto.PathUniversalMCP + `</code> instead.`
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
		t.Fatalf("universal consent page = %d, want 200 with %s:\n%s", resp.StatusCode, want, page)
	}
}

func TestConsentPageWithoutNoticeIsUnchanged(t *testing.T) {
	seen := 0
	for _, st := range consentStates() {
		if st.page.Retiring != nil {
			continue
		}
		seen++
		var b bytes.Buffer
		if err := consentTemplate.Execute(&b, st.page); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b.Bytes())); got != mainConsentPages[st.name] {
			t.Errorf("%s with no notice renders %s, not main's page %s:\n%s", st.name, got, mainConsentPages[st.name], b.String())
		}
	}
	if seen != len(mainConsentPages) {
		t.Errorf("checked %d states with no notice, want %d", seen, len(mainConsentPages))
	}
}

func TestConsentPageKeepsOnlyNoNotice(t *testing.T) {
	e := newEnv(t)
	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	page := func() string {
		t.Helper()
		resp, err := http.Get(e.authorizeURL())
		if err != nil {
			t.Fatal(err)
		}
		page, err := readBody(resp)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	retiring := `<p class="retiring">This address is retiring.`
	if strings.Contains(page(), "retiring") {
		t.Fatal("consent page warns with no notice")
	}
	e.edge.set(nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}))
	e.skew.Add(int64(noticeReadEvery - time.Second))
	if strings.Contains(page(), "retiring") {
		t.Fatal("consent page read the notices again within a minute of finding none")
	}
	e.skew.Add(int64(time.Second))
	if !strings.Contains(page(), retiring) {
		t.Fatal("consent page does not warn a minute after the notice came")
	}
	e.edge.set(nil, edgeagent.StoredNotices{})
	if strings.Contains(page(), "retiring") {
		t.Fatal("consent page kept a notice the edge withdrew")
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
	add := func(member domain.MemberID, resource string) string {
		c := &store.Connector{Member: member, ClientID: e.clientID, ClientName: "Test client", RedirectHost: e.clientHost,
			Resource: resource, Scopes: []string{"read"}, CreatedAt: now}
		if err := e.db.CreateConnector(ctx, c, store.ConnectorTokens{Access: tokenHash(string(member) + resource + "a"), AccessExpiresAt: now.Add(time.Hour),
			Refresh: tokenHash(string(member) + resource + "r"), RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	oldPort := "https://relay-old.example.test:443" + ownPath
	resources := []string{e.base, e.origin + edgeproto.PathUniversalMCP, newRelay + ownPath, oldRelay + ownPath, oldRelay + edgeproto.PathUniversalMCP, oldPort}
	ids := map[string]string{}
	for _, r := range resources {
		ids[add(member.ID, r)] = r
	}
	other := &domain.Member{DisplayName: "Bea", TailnetLogin: "bea@example.test", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(ctx, other); err != nil {
		t.Fatal(err)
	}
	add(other.ID, e.base)
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
		name      string
		version   int64
		unclaimed bool
		relays    []string
		notices   edgeagent.StoredNotices
		err       error
		want      want
	}{
		{"no notice", 2, false, both, edgeagent.StoredNotices{}, nil, want{url: e.base}},
		{"no notice, universal", 6, false, both, edgeagent.StoredNotices{}, nil, want{url: universal, universal: universal}},
		{"unreadable notices", 6, false, both, edgeagent.StoredNotices{}, errors.New("read notices.json: permission denied"), want{url: universal, universal: universal}},
		{"enrolled at a retiring address", 6, false, both, stored(false, retiring), nil, want{url: newRelay + "/mcp", universal: newRelay + "/mcp",
			notices: at("retiring", "2027-03-01", newRelay+ownPath, newRelay+"/mcp")}},
		{"replacement announced with the default port", 6, false, []string{e.origin, newRelay + ":443"}, stored(false, retiring), nil,
			want{url: newRelay + ":443/mcp", universal: newRelay + ":443/mcp",
				notices: at("retiring", "2027-03-01", newRelay+":443"+ownPath, newRelay+":443/mcp")}},
		{"retiring without a date", 6, false, both, stored(false, undated), nil, want{url: newRelay + "/mcp", universal: newRelay + "/mcp",
			notices: at("retiring", "", newRelay+ownPath, newRelay+"/mcp")}},
		{"first use", 6, false, both, stored(true, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"replacement not announced", 6, false, nil, stored(false, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"edge before relay origins", 3, false, both, stored(false, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"unclaimed", 6, true, both, stored(false, retiring), nil, want{url: universal, universal: universal,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"edge offline", 0, false, both, stored(false, retiring), nil, want{url: e.base,
			notices: at("retiring", "2027-03-01", "", "")}},
		{"enrolled at the replacement of a retired address", 6, false, nil, stored(false, retired), nil, want{url: universal, universal: universal,
			notices: map[string]*protocol.ConnectorNotice{
				oldRelay + ownPath: {State: "retired", URL: e.origin + ownPath},
				oldRelay + "/mcp":  {State: "retired", URL: e.origin + "/mcp"},
				oldPort:            {State: "retired", URL: e.origin + ownPath},
			}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.edge.version.Store(tt.version)
			e.edge.unclaimed.Store(tt.unclaimed)
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

	// A member with no GitHub account bound is not offered the universal
	// URL at the edge, so no notice names it to them.
	e.edge.version.Store(6)
	e.edge.unclaimed.Store(false)
	e.edge.set(both, stored(false, retiring))
	e.edge.setErr(nil)
	if _, err := e.db.UnlinkIdentity(ctx, member.ID, "github", "1"); err != nil {
		t.Fatal(err)
	}
	var list protocol.ConnectorListResult
	if err := e.admin.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
		t.Fatal(err)
	}
	if list.URL != newRelay+ownPath || list.UniversalURL != e.origin+"/mcp" {
		t.Errorf("url %q universal_url %q, want %q %q", list.URL, list.UniversalURL, newRelay+ownPath, e.origin+"/mcp")
	}
	for _, c := range list.Connectors {
		got, _ := json.Marshal(c.Notice)
		want, _ := json.Marshal(map[string]*protocol.ConnectorNotice{
			e.base:            {State: "retiring", URL: newRelay + ownPath, Date: "2027-03-01"},
			e.origin + "/mcp": {State: "retiring", Date: "2027-03-01"},
		}[ids[c.ID]])
		if string(got) != string(want) {
			t.Errorf("without GitHub, connector at %s: notice %s, want %s", ids[c.ID], got, want)
		}
	}
}

func TestConnectorCodeNeverMintsWithoutAURL(t *testing.T) {
	e := newEnv(t)
	for k := int64(1); k <= 6; k++ {
		e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
		e.edge.dropAfter.Store(k)
		var code protocol.ConnectorCodeResult
		if err := e.device.Call(protocol.MethodConnectorCode, struct{}{}, &code); err == nil && code.URL == "" {
			t.Fatalf("edge dropping after %d version reads: connector.code minted %+v with no URL", k, code)
		}
		e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
		e.edge.dropAfter.Store(k)
		var list protocol.ConnectorListResult
		err := e.device.Call(protocol.MethodConnectorList, struct{}{}, &list)
		// One read of the edge answers for the URL, the universal URL and
		// sign_in together: the member has GitHub linked, so the URL is the
		// universal one whenever there is one, and edge-access
		// approved-devices takes no sign-in.
		if err != nil || list.URL == "" || (list.UniversalURL != "" && list.URL != list.UniversalURL) ||
			(list.UniversalURL == "" && list.URL != e.base) || list.SignIn {
			t.Fatalf("edge dropping after %d version reads: connector.list = %+v, %v; want one read's URL, universal URL and sign_in", k, list, err)
		}
	}
	e.edge.dropAfter.Store(0)
}

func TestUnreadableNoticesLogOncePerFailure(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	list := func() {
		t.Helper()
		var out protocol.ConnectorListResult
		if err := e.device.Call(protocol.MethodConnectorList, struct{}{}, &out); err != nil {
			t.Fatal(err)
		}
	}
	warned := func(want int) {
		t.Helper()
		if got := strings.Count(logs.String(), "connectors shown without the edge's notices"); got != want {
			t.Fatalf("logged the unreadable notices %d times, want %d:\n%s", got, want, logs.String())
		}
	}
	failing := errors.New("read notices.json: permission denied")
	e.edge.setErr(failing)
	list()
	list()
	warned(1)
	e.edge.setErr(nil)
	list()
	e.edge.setErr(failing)
	list()
	warned(2)
}

func TestToolResultNoticeAtRetiringAddress(t *testing.T) {
	e := newEnv(t)
	c, other := e.connect(t), e.connect(t)
	session, refresh := c.session, c.token.RefreshToken
	// later moves the clock on by d and connects again with a refreshed
	// token, since the access token lasts an hour.
	later := func(d time.Duration) {
		t.Helper()
		e.skew.Add(int64(d))
		next := e.refresh(t, c, refresh, http.StatusOK)
		session, refresh = e.session(t, next.AccessToken), next.RefreshToken
	}
	// set gives the edge notices and moves the clock past the last read
	// of them.
	set := func(relays []string, n edgeagent.StoredNotices) {
		e.edge.set(relays, n)
		e.skew.Add(int64(noticeReadEvery))
	}
	callOn := func(s *mcp.ClientSession) *mcp.CallToolResult {
		t.Helper()
		res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_list_workspaces"})
		if err != nil || res.IsError {
			t.Fatalf("aether_list_workspaces = %+v %v", res, err)
		}
		return res
	}
	plain := callOn(session)
	if len(plain.Content) != 1 {
		t.Fatalf("with no notice the result has %d content blocks, want 1", len(plain.Content))
	}
	first, _ := json.Marshal(plain.Content[0])
	structured, _ := json.Marshal(plain.StructuredContent)
	blocksOn := func(s *mcp.ClientSession, want int) *mcp.CallToolResult {
		t.Helper()
		res := callOn(s)
		if len(res.Content) != want {
			t.Fatalf("result has %d content blocks, want %d: %+v", len(res.Content), want, res.Content)
		}
		gotFirst, _ := json.Marshal(res.Content[0])
		gotStructured, _ := json.Marshal(res.StructuredContent)
		if string(gotFirst) != string(first) || string(gotStructured) != string(structured) {
			t.Fatalf("first block %s structured %s, want %s %s", gotFirst, gotStructured, first, structured)
		}
		return res
	}
	blocks := func(want int) *mcp.CallToolResult { t.Helper(); return blocksOn(session, want) }
	notice := func(res *mcp.CallToolResult) string {
		t.Helper()
		text, ok := res.Content[1].(*mcp.TextContent)
		if !ok {
			t.Fatalf("second block is %T, want text", res.Content[1])
		}
		return text.Text
	}

	set(nil, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: oldRelay, Replacement: e.origin, Date: "2027-03-01"}))
	blocks(1)

	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	both := []string{e.origin, newRelay}
	set(both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: e.origin, Replacement: newRelay}))
	blocks(1)
	retiring := stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay, Date: "2027-03-01"})
	set(nil, retiring)
	blocks(1)
	set(both, stored(true, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}))
	blocks(1)

	set(both, retiring)
	for _, name := range []string{"search", "fetch"} {
		if !jsonOnly(&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name}}) {
			t.Fatalf("%s may carry the notice", name)
		}
	}
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "x"}})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("search = %+v %v, want one content block", res, err)
	}
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_get_session", Arguments: map[string]any{"run_id": "run-none"}})
	if err != nil || !res.IsError || len(res.Content) != 1 {
		t.Fatalf("aether_get_session of no run = %+v %v, want one error block", res, err)
	}
	newURL := newRelay + strings.TrimPrefix(e.base, e.origin)
	want := "Notice for the user: this connector was added at " + e.base + ", an address that is retiring on 2027-03-01. It must be added again at " + newURL + "."
	if got := notice(blocks(2)); got != want {
		t.Fatalf("notice %q, want %q", got, want)
	}
	blocks(1)
	if got := notice(blocksOn(other.session, 2)); got != want {
		t.Fatalf("second connector's notice %q, want %q", got, want)
	}
	blocksOn(other.session, 1)
	blocks(1)

	later(noticeEvery - noticeReadEvery - time.Second)
	blocks(1)
	set(both, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}))
	later(time.Second)
	if got, want := notice(blocks(2)), "Notice for the user: this connector was added at "+e.base+
		", an address that is retiring. It must be added again at "+newURL+"."; got != want {
		t.Fatalf("notice %q, want %q", got, want)
	}
	blocks(1)
}

func TestToolResultNoticeAtUniversalURLNeedsGitHub(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	member, err := e.db.GetMemberByIdentity(ctx, "github", "1")
	if err != nil {
		t.Fatal(err)
	}
	universal := e.origin + edgeproto.PathUniversalMCP
	now := time.Now()
	// at is a session of a connector of member added at the universal URL.
	at := func(token string) *mcp.ClientSession {
		t.Helper()
		c := &store.Connector{Member: member.ID, ClientID: e.clientID, ClientName: "Test client", RedirectHost: e.clientHost,
			Resource: universal, Scopes: []string{"read"}, CreatedAt: now}
		if err := e.db.CreateConnector(ctx, c, store.ConnectorTokens{Access: tokenHash(token), AccessExpiresAt: now.Add(time.Hour),
			Refresh: tokenHash(token + "r"), RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
		httpClient := &http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})}}
		cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: universal, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}
	blocks := func(cs *mcp.ClientSession) int {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "aether_list_workspaces"})
		if err != nil || res.IsError {
			t.Fatalf("aether_list_workspaces = %+v %v", res, err)
		}
		return len(res.Content)
	}
	linked, unlinked := at("universal-linked"), at("universal-unlinked")
	e.edge.version.Store(int64(edgeproto.VersionRelayOrigin))
	e.edge.set([]string{e.origin, newRelay}, stored(false, edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: newRelay}))
	if n := blocks(linked); n != 2 {
		t.Fatalf("with GitHub linked the result has %d content blocks, want 2", n)
	}
	if _, err := e.db.UnlinkIdentity(ctx, member.ID, "github", "1"); err != nil {
		t.Fatal(err)
	}
	if n := blocks(unlinked); n != 1 {
		t.Fatalf("without GitHub the result has %d content blocks, want 1: the edge does not offer the universal URL", n)
	}
}
