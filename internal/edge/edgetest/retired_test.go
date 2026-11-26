package edgetest

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"

	edgeagent "github.com/3xDevOps/cloudfield/internal/edge/agent"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edge "github.com/3xDevOps/cloudfield/internal/edge/service"
	"github.com/3xDevOps/cloudfield/internal/sshd"
)

// TestRetiredPair takes an edge with two pairs, A and B, through
// retiring A and retiring it for good. Before either option is set,
// nothing records an origin. While A is retiring, a server moves to B
// by enrolling there again. Once A is retired and B is the only pair
// served: every path of A answers 410 naming B; the server at B gets a
// signed retired notice and no longer hears of A; what A issued and what
// has no origin is deleted while B's device keeps working; a server that
// stayed at A reads the 410 text in its status; a connector added at A
// fails and one at B works. Started again without the option, as an
// older image, the edge answers 421 on A's hosts.
func TestRetiredPair(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	signinA, relayA, signinB, relayB := h.signinURL, h.relayURL, h.signinB, h.relayB
	edgeDB, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.Join(h.edgeDir, "edge.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edgeDB.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := edgeDB.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	stays := h.newServer(edgeproto.PolicyAccount)
	connectors := newConnectorClient(t)
	policy := edgeproto.PolicyApprovedDevices
	moves := h.newConnectorServer(policy, connectors, sshd.Services{})
	alA, noOrigin := h.login(alice)[0], h.login(alice)[0]
	h.claimServer(alA, moves)
	h.signinURL, h.relayURL = signinB, relayB
	alB := h.login(alice)[0]
	h.signinURL, h.relayURL = signinA, relayA
	if n := count(`SELECT COUNT(*) FROM devices WHERE signin_origin IS NOT NULL`) + count(`SELECT COUNT(*) FROM origin_pairs`); n != 0 {
		t.Fatalf("with neither option set the edge recorded %d origins", n)
	}

	h.stopEdge()
	h.retiring = []edge.Retiring{{RelayOrigin: relayA, Replacement: relayB}}
	h.startEdge()
	backOnline(t, h, moves)
	if _, err = alA.edge.Servers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := alA.edge.Retiring(); !ok || got.Replacement != relayB {
		t.Fatalf("client at A while A is retiring: %+v, %v", got, ok)
	}
	if _, err = alB.edge.Servers(t.Context()); err != nil {
		t.Fatal(err)
	}
	browserA, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	baseA, baseB := relayA+edgeproto.MCPPath(moves.id), relayB+edgeproto.MCPPath(moves.id)
	tokenA := connectAt(t, h, connectors, alA, moves, baseA)
	moves.stop()
	moves.edge = relayB
	moves.start()
	tokenB := connectAt(t, h, connectors, alA, moves, baseB)
	sessionA := ""
	for _, c := range browserA.client.Jar.Cookies(mustURL(t, signinA)) {
		if c.Name == "__Host-aether_edge_session" {
			sessionA = c.Value
		}
	}
	if sessionA == "" || count(`SELECT COUNT(*) FROM web_sessions WHERE signin_origin = ?`, signinA) != 1 {
		t.Fatalf("the browser at A holds no session the edge recorded at A")
	}
	devA, devB, devNo := deviceID(t, alA), deviceID(t, alB), deviceID(t, noOrigin)

	h.stopEdge()
	h.retiring, h.onlyB = nil, true
	h.retired = []edge.Retired{{SigninOrigin: signinA, RelayOrigin: relayA, Replacement: relayB}}
	from := h.proxy.mark()
	h.startEdge()
	h.signinURL, h.relayURL = signinB, relayB
	backOnline(t, h, moves)

	text := relayA + " is retired; this edge is at " + relayB + ". Move a server with: cloudfield-server edge move " + relayB +
		". Sign a device in with: cloudfield login --edge " + relayB
	for _, p := range []struct{ origin, path string }{
		{signinA, "/signin"}, {signinA, "/servers"}, {signinA, edgeproto.PathServers}, {signinA, edgeproto.PathDeviceStart},
		{relayA, edgeproto.PathEdgeInfo}, {relayA, edgeproto.PathServerControl}, {relayA, edgeproto.ConnectPath(moves.id)},
		{relayA, "/join/" + moves.id + "/inv-abcdefghjk"}, {relayA, edgeproto.MCPPath(moves.id)}, {relayA, edgeproto.PathUniversalMCP},
	} {
		resp, body := mcpRequest(t, http.MethodGet, p.origin+p.path, http.Header{edgeproto.HeaderVersion: {strconv.Itoa(edgeproto.Version)}}, nil)
		old := relayA
		if p.origin == signinA {
			old = signinA
		}
		want := strings.Replace(text, relayA+" is retired", old+" is retired", 1)
		if resp.StatusCode != http.StatusGone || !strings.Contains(body, want) || !strings.Contains(body, `"retired":{"replacement":"`+relayB+`"}`) ||
			resp.Header.Get("Location") != "" || resp.Header.Get("WWW-Authenticate") != "" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s%s: %s %v %s", p.origin, p.path, resp.Status, resp.Header, body)
		}
	}
	resp, page := mcpRequest(t, http.MethodGet, signinA+"/servers", http.Header{"Accept": {"text/html"}, "Cookie": {"__Host-aether_edge_session=" + sessionA}}, nil)
	if resp.StatusCode != http.StatusGone || !strings.Contains(page, "<code>"+signinA+"</code> is retired. This edge is now at <code>"+signinB+"</code>: sign in there.") {
		t.Errorf("browser page at A: %s\n%s", resp.Status, page)
	}
	_, ws, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(relayA, "http")+edgeproto.PathServerControl, nil)
	if ws == nil || ws.StatusCode != http.StatusGone || err == nil {
		t.Errorf("control dial at A: %v %v, want 410", ws, err)
	}

	ready := h.readyTo(t, moves, from)
	if want := (edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: relayA, Replacement: relayB}); !slices.Equal(ready.Notices, edgeproto.Notices{want}) ||
		slices.Contains(ready.RelayOrigins, relayA) {
		t.Errorf("ready to the server at B: notices %+v, relay origins %v; want %+v and no %s", ready.Notices, ready.RelayOrigins, want, relayA)
	}
	if got, nerr := moves.agent.Notices(); nerr != nil || len(got.Notices) != 1 || got.Notices[0].State != edgeproto.NoticeRetired {
		t.Errorf("server at B stored %+v, %v", got, nerr)
	}

	for id, want := range map[string]int{devA: 0, devNo: 0, devB: 1} {
		if got := count(`SELECT COUNT(*) FROM devices WHERE id = ?`, id); got != want {
			t.Errorf("device %s: %d rows, want %d", id, got, want)
		}
	}
	if n := count(`SELECT COUNT(*) FROM web_sessions WHERE signin_origin = ? OR signin_origin IS NULL`, signinA); n != 0 {
		t.Errorf("%d sessions of A or with no origin left", n)
	}
	if _, err = alB.edge.Servers(t.Context()); err != nil {
		t.Errorf("device at B: %v", err)
	}
	_, err = alA.edge.Servers(t.Context())
	var refused *edgeclient.RefusedError
	if want := strings.Replace(text, relayA+" is retired", signinA+" is retired", 1); !errors.As(err, &refused) || refused.Status != http.StatusGone || refused.Message != want {
		t.Errorf("device at A: %v, want 410 %q", err, want)
	}
	stays.waitStatus("the server that stayed at A reads the 410", func(st edgeagent.Status) error {
		if want := "410 Gone: " + text; !strings.Contains(st.Error, want) {
			return fmt.Errorf("status error %q lacks %q", st.Error, want)
		}
		return nil
	})
	if resp, body := toolsList(t, baseA, tokenA.AccessToken, nil); resp.StatusCode != http.StatusGone || !strings.Contains(body, text) {
		t.Errorf("connector at A: %s %s", resp.Status, body)
	}
	mustCall(t, baseB, tokenB.AccessToken)
	counts := map[string]uint64{}
	for _, r := range h.retiredRequests() {
		counts[r.Origin+" "+r.Kind] = r.Count
	}
	for _, k := range []string{relayA + " server", relayA + " connector", signinA + " client", signinA + " browser"} {
		if counts[k] == 0 {
			t.Errorf("no retired request counted for %s: %v", k, counts)
		}
	}

	h.stopEdge()
	h.retired = nil
	h.startEdge()
	h.signinURL, h.relayURL = signinB, relayB
	for _, target := range []string{signinA + "/signin", signinA + edgeproto.PathServers, relayA + edgeproto.PathEdgeInfo, relayA + edgeproto.MCPPath(moves.id)} {
		if resp, body := mcpRequest(t, http.MethodGet, target, nil, nil); resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("rolled back, %s: %s %s, want 421", target, resp.Status, body)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func deviceID(t *testing.T, c *client) string {
	t.Helper()
	s, err := c.edge.Session()
	if err != nil {
		t.Fatal(err)
	}
	return s.Device.ID
}

func (h *harness) retiredRequests() []edge.RetiredRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.node.svc.RetiredRequests()
}
