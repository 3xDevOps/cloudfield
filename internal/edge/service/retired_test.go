package edge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/edge/relay"
)

// The retired pair of a test edge, which serves testSignin and testRelay.
const (
	oldSignin = "https://signin-old.example.test"
	oldRelay  = "https://relay-old.example.test"
)

func newRetiredHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, func(cfg *Config, _ *fakeProvider) {
		cfg.Retired = []Retired{{SigninOrigin: oldSignin, RelayOrigin: oldRelay, Replacement: testRelay}}
	})
}

func retiredJSON(old string) string {
	return `{"error":"` + old + ` is retired; this edge is at ` + testRelay + `. Move a server with: cloudfield-server edge move ` + testRelay +
		`. Sign a device in with: cloudfield login --edge ` + testRelay + `","retired":{"replacement":"` + testRelay + `"}}` + "\n"
}

func retiredPage(old, address string, signin, join bool) string {
	s := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>This address is retired - Cloudfield Edge</title>
</head>
<body>
<main>
<h1>This address is retired</h1>
<p><code>` + old + `</code> is retired. This edge is now at <code>` + address + `</code>`
	if signin {
		s += `: sign in there`
	}
	s += ".</p>\n"
	if join {
		s += "<p>This link was made at the retired address and no longer works. Ask whoever sent it for a new one.</p>\n"
	}
	return s + `<p>Move a server with: <code>cloudfield-server edge move ` + testRelay + `</code></p>
<p>Sign a device in with: <code>cloudfield login --edge ` + testRelay + `</code></p>
</main>
</body>
</html>
`
}

func (h *harness) retiredDo(t *testing.T, method, rawURL string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(`{"label":"laptop"}`))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

// TestRetiredAnswers requests every kind of path on both hosts of a
// retired pair: each gets 410 Gone naming the replacement, as JSON or,
// for a browser page, as a static page that reflects nothing of the
// request and clears the edge's cookies. The store is closed first, so
// no answer reads it.
func TestRetiredAnswers(t *testing.T) {
	h := newRetiredHarness(t)
	if err := h.svc.store.Close(); err != nil {
		t.Fatal(err)
	}
	serverID := testServerID(t)
	html := http.Header{"Accept": {"text/html,application/xhtml+xml"}}
	api := http.Header{edgeproto.HeaderVersion: {"6"}, "Authorization": {"Bearer " + strings.Repeat("a", 43)}}
	ws := http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}, "Accept": {"text/html"}}
	for _, tt := range []struct {
		method, origin, path string
		header               http.Header
		page                 string
		kind                 string
	}{
		{http.MethodGet, oldSignin, "/servers?next=<script>", html, retiredPage(oldSignin, testSignin, true, false), RetiredBrowser},
		{http.MethodGet, oldSignin, "/", html, retiredPage(oldSignin, testSignin, true, false), RetiredBrowser},
		{http.MethodGet, oldSignin, "/signin/github/callback?code=c0de&state=s", html, retiredPage(oldSignin, testSignin, true, false), RetiredBrowser},
		{http.MethodGet, oldSignin, "/device", html, retiredPage(oldSignin, testSignin, true, false), RetiredBrowser},
		{http.MethodGet, oldSignin, "/edge.css", nil, "", RetiredBrowser},
		{http.MethodPost, oldSignin, "/device/confirm", html, "", RetiredBrowser},
		{http.MethodPost, oldSignin, edgeproto.PathDeviceStart, api, "", RetiredClient},
		{http.MethodPost, oldSignin, edgeproto.PathDeviceToken, api, "", RetiredClient},
		{http.MethodGet, oldSignin, edgeproto.PathServers, api, "", RetiredClient},
		{http.MethodGet, oldSignin, edgeproto.PathServers, html, "", RetiredClient},
		{http.MethodGet, oldSignin, edgeproto.PathMCPChoose + "?server=x", html, retiredPage(oldSignin, testSignin, true, false), RetiredConnector},
		{http.MethodGet, oldRelay, "/join/" + serverID + "/inv-abcdefghjk", html, retiredPage(oldRelay, testRelay, false, true), RetiredBrowser},
		{http.MethodGet, oldRelay, "/no/such/page", html, retiredPage(oldRelay, testRelay, false, false), RetiredBrowser},
		{http.MethodGet, oldRelay, edgeproto.PathServerControl, ws, "", RetiredServer},
		{http.MethodGet, oldRelay, "/v1/server/data/conn-1", ws, "", RetiredServer},
		{http.MethodGet, oldRelay, edgeproto.ConnectPath(serverID), ws, "", RetiredClient},
		{http.MethodGet, oldRelay, edgeproto.PathEdgeInfo, nil, "", RetiredClient},
		{http.MethodPost, oldRelay, "/mcp/" + serverID, api, "", RetiredConnector},
		{http.MethodPost, oldRelay, edgeproto.PathUniversalMCP, nil, "", RetiredConnector},
		{http.MethodGet, oldRelay, edgeproto.PathUniversalMCPAuthorize + "?client_id=x&redirect_uri=https://claude.ai/cb", html, retiredPage(oldRelay, testRelay, false, false), RetiredConnector},
		{http.MethodGet, oldRelay, "/.well-known/oauth-protected-resource/mcp/" + serverID, nil, "", RetiredConnector},
		{http.MethodGet, oldRelay, edgeproto.PathOpenAIChallenge, nil, "", RetiredConnector},
	} {
		before := h.retiredCount(tt.origin, tt.kind)
		resp, body := h.retiredDo(t, tt.method, tt.origin+tt.path, tt.header)
		name := tt.method + " " + tt.origin + tt.path
		if resp.StatusCode != http.StatusGone {
			t.Errorf("%s: %s, want 410 Gone\n%s", name, resp.Status, body)
		}
		hdr := resp.Header
		for _, k := range []string{"Location", "WWW-Authenticate", edgeproto.HeaderRetiring} {
			if v := hdr.Values(k); v != nil {
				t.Errorf("%s carries %s %q", name, k, v)
			}
		}
		if hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s headers %v", name, hdr)
		}
		if tt.page == "" {
			if want := retiredJSON(tt.origin); body != want || hdr.Get("Content-Type") != "application/json" || hdr.Values("Set-Cookie") != nil {
				t.Errorf("%s: %s %q %q\nwant %s", name, hdr.Get("Content-Type"), hdr.Values("Set-Cookie"), body, want)
			}
		} else {
			if body != tt.page || hdr.Get("Content-Type") != "text/html; charset=utf-8" {
				t.Errorf("%s: %s\n%s\nwant\n%s", name, hdr.Get("Content-Type"), body, tt.page)
			}
			if got := hdr.Get("Content-Security-Policy"); got != "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'" {
				t.Errorf("%s: Content-Security-Policy %q", name, got)
			}
			if hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("Strict-Transport-Security") == "" {
				t.Errorf("%s headers %v", name, hdr)
			}
			want := []string{
				"__Host-aether_edge_session=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax",
				"__Host-aether_edge_signin=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax",
				"__Host-aether_edge_browser=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax",
			}
			if got := hdr.Values("Set-Cookie"); !slices.Equal(got, want) {
				t.Errorf("%s sets cookies %q, want %q", name, got, want)
			}
			if strings.Contains(body, "script") || strings.Contains(body, "c0de") || strings.Contains(body, "claude.ai") || strings.Contains(body, serverID) {
				t.Errorf("%s reflects the request:\n%s", name, body)
			}
		}
		if got := h.retiredCount(tt.origin, tt.kind); got != before+1 {
			t.Errorf("%s counted %d %s requests, want %d", name, got, tt.kind, before+1)
		}
	}
	for _, origin := range []string{oldSignin, oldRelay} {
		if resp, body := h.retiredDo(t, http.MethodGet, origin+"/healthz", nil); resp.StatusCode != http.StatusOK || body != "ok\n" {
			t.Errorf("%s/healthz: %s %q", origin, resp.Status, body)
		}
	}
	if got := len(h.svc.RetiredRequests()); got != 8 {
		t.Errorf("RetiredRequests has %d rows, want 4 kinds for each of 2 hosts", got)
	}
}

func (h *harness) retiredCount(origin, kind string) uint64 {
	for _, r := range h.svc.RetiredRequests() {
		if r.Origin == origin && r.Kind == kind {
			return r.Count
		}
	}
	return 0
}

// TestRetiredNotices lists a retired notice for each retired relay
// origin after the retiring ones, and the edge refuses a retired pair
// whose replacement is not a served pair that stays or whose host is
// still served.
func TestRetiredNotices(t *testing.T) {
	h := newHarness(t, func(cfg *Config, p *fakeProvider) {
		app := p.app()
		app.ClientID, app.ClientSecret = "test-client-2", "test-secret-2"
		cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
		cfg.Retiring = []Retiring{{RelayOrigin: testRelay2, Replacement: testRelay, Date: "2027-03-01"}}
		cfg.Retired = []Retired{{SigninOrigin: oldSignin, RelayOrigin: oldRelay + ":443", Replacement: testRelay}}
	})
	want := []edgeproto.Notice{
		{State: edgeproto.NoticeRetiring, Origin: testRelay2, Replacement: testRelay, Date: "2027-03-01"},
		{State: edgeproto.NoticeRetired, Origin: oldRelay + ":443", Replacement: testRelay},
	}
	if !slices.Equal(h.svc.notices, want) {
		t.Errorf("notices %+v, want %+v", h.svc.notices, want)
	}
	if cfg := h.svc.RelayConfig(0); !slices.Equal(cfg.Notices, want) || slices.Contains(cfg.MoreOrigins, oldRelay) {
		t.Errorf("relay config notices %+v, more origins %v", cfg.Notices, cfg.MoreOrigins)
	}
	if h := newHarness(t); h.svc.notices != nil {
		t.Errorf("nothing retired: notices %+v", h.svc.notices)
	}

	for _, tt := range []struct {
		retired Retired
		want    string
	}{
		{Retired{SigninOrigin: oldSignin, RelayOrigin: oldRelay, Replacement: oldRelay},
			"edge: retired " + oldRelay + " in favour of " + oldRelay + ": the replacement must be the relay origin of a pair this edge serves that is not retiring"},
		{Retired{SigninOrigin: oldSignin, RelayOrigin: oldRelay, Replacement: testRelay2},
			"edge: retired " + oldRelay + " in favour of " + testRelay2 + ": the replacement must be the relay origin of a pair this edge serves that is not retiring"},
		{Retired{SigninOrigin: oldSignin, RelayOrigin: "https://signin.example.test:8443", Replacement: testRelay},
			"edge: the retired origin https://signin.example.test:8443 has the host name signin.example.test of another origin; each host name is served or retired once"},
		{Retired{SigninOrigin: oldSignin, RelayOrigin: oldSignin, Replacement: testRelay},
			"edge: the retired origin " + oldSignin + " has the host name signin-old.example.test of another origin; each host name is served or retired once"},
	} {
		cfg := Config{DataDir: t.TempDir(), SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: &OAuthApp{ClientID: "id", ClientSecret: "secret"},
			MorePairs: []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: &OAuthApp{ClientID: "id2", ClientSecret: "secret2"}}},
			Retiring:  []Retiring{{RelayOrigin: testRelay2, Replacement: testRelay}},
			Retired:   []Retired{tt.retired}}
		if svc, err := New(cfg); err == nil || err.Error() != tt.want {
			if svc != nil {
				_ = svc.Close()
			}
			t.Errorf("retired %+v: %v, want %q", tt.retired, err, tt.want)
		}
	}
}

// TestRetiredDeletesSignIns signs in a device and a browser at each pair
// of a two-pair edge, adds a device, a session and an authorization with
// no origin, then restarts the edge with the second pair retired: what
// the second pair issued and what has no origin is gone, the first
// pair's stays, and the servers and accounts stay.
func TestRetiredDeletesSignIns(t *testing.T) {
	h := newRecordingHarness(t)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	var tokens []string
	for _, origin := range []string{testSignin, testSignin2} {
		b := h.browser(t)
		b.origin = origin
		b.signIn(t)
		var start edgeproto.DeviceStartResponse
		if status, msg := h.apiCallOn(t, origin, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "laptop"}, &start); status != http.StatusOK {
			t.Fatalf("device start on %s: %d %s", origin, status, msg)
		}
		b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
		h.clock.Advance(pollInterval)
		var tok edgeproto.DeviceTokenResponse
		if status, msg := h.apiCallOn(t, origin, http.MethodPost, edgeproto.PathDeviceToken, "",
			edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode}, &tok); status != http.StatusOK {
			t.Fatalf("device token on %s: %d %s", origin, status, msg)
		}
		tokens = append(tokens, tok.Token)
		if status, msg := h.apiCallOn(t, origin, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "pending"}, nil); status != http.StatusOK {
			t.Fatalf("pending device start on %s: %d %s", origin, status, msg)
		}
	}
	id := testServerID(t)
	h.claim(t, id, "workstation", edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "owner"})
	h.exec(t, `INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at)
		SELECT ?, 'hash-no-origin', account_id, public_key, label, created_at, last_used_at FROM devices LIMIT 1`, "dev-no-origin")
	h.exec(t, `INSERT INTO web_sessions (id, token_hash, account_id, created_at, expires_at)
		SELECT 'sess-no-origin', 'hash-no-origin', account_id, created_at, expires_at FROM web_sessions LIMIT 1`)
	h.exec(t, `INSERT INTO device_authorizations (code_hash, user_code_hash, label, public_key, client_addr, status, created_at, expires_at, last_poll_at)
		SELECT 'code-no-origin', 'user-no-origin', label, public_key, client_addr, status, created_at, expires_at, 0 FROM device_authorizations LIMIT 1`)
	count := func(query string) string { return h.column(t, query) }
	if got := count(`SELECT COUNT(*) || ' ' || (SELECT COUNT(*) FROM web_sessions) || ' ' || (SELECT COUNT(*) FROM device_authorizations) FROM devices`); got != "3 3 3" {
		t.Fatalf("before: devices, sessions, authorizations %s", got)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Config{DataDir: h.dataDir, SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: h.provider.app(), Clock: h.clock.Now,
		Retired: []Retired{{SigninOrigin: testSignin2 + ":443", RelayOrigin: testRelay2, Replacement: testRelay}}})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.recordOrigins {
		t.Error("an edge with a retired pair does not record origins")
	}
	t.Cleanup(func() { _ = svc.Close() })
	for q, want := range map[string]string{
		`SELECT group_concat(signin_origin) FROM devices`:               testSignin,
		`SELECT group_concat(signin_origin) FROM web_sessions`:          testSignin,
		`SELECT group_concat(signin_origin) FROM device_authorizations`: testSignin,
		`SELECT COUNT(*) FROM servers WHERE id = '` + id + `'`:          "1",
		`SELECT COUNT(*) FROM accounts`:                                 "1",
	} {
		if got := count(q); got != want {
			t.Errorf("after start with %s retired: %s = %q, want %q", testSignin2, q, got, want)
		}
	}
	ctx := context.Background()
	if _, _, err = svc.deviceByToken(ctx, tokens[1]); err == nil {
		t.Errorf("the device token of %s still authenticates", testSignin2)
	}
	if _, _, err = svc.deviceByToken(ctx, tokens[0]); err != nil {
		t.Errorf("the device token of %s: %v", testSignin, err)
	}
}

// TestDeviceStartRelayOrigin starts device sign-ins on the first pair of
// a two-pair edge naming each relay origin: only its own pair's, in
// either spelling, or none, is accepted.
func TestDeviceStartRelayOrigin(t *testing.T) {
	h := newPairsHarness(t)
	for _, tt := range []struct{ relay, want string }{
		{"", ""},
		{testRelay, ""},
		{"https://RELAY.example.test:443", ""},
		{testRelay2, testSignin + " signs devices in for the relay origin " + testRelay + ", not for " + testRelay2 +
			"; sign in with: cloudfield login --edge <the relay origin you use>"},
		{testSignin, testSignin + " signs devices in for the relay origin " + testRelay + ", not for " + testSignin},
		{"https://relay.example.test:8443", "not for https://relay.example.test:8443"},
		{"relay.example.test", `relay_origin: edgeproto: edge url "relay.example.test" must be https://host[:port]`},
	} {
		h.clock.Advance(pollInterval * 6)
		status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Label: "laptop", Key: newDeviceKey(t), RelayOrigin: tt.relay}, nil)
		switch {
		case tt.want == "" && status != http.StatusOK:
			t.Errorf("relay_origin %q: %d %s", tt.relay, status, msg)
		case tt.want != "" && (status != http.StatusBadRequest || !strings.Contains(msg, tt.want)):
			t.Errorf("relay_origin %q: %d %q, want 400 %q", tt.relay, status, msg, tt.want)
		}
	}
}

// TestDeviceAuthBoundToPair starts a device sign-in on the first pair of
// a two-pair edge that records origins: it is approved and polled there
// only. Without recording, the authorization has no pair, and the other
// pair confirms it as before.
func TestDeviceAuthBoundToPair(t *testing.T) {
	h := newRecordingHarness(t)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	start := startDevice(t, h)
	other := h.browser(t)
	other.origin = testSignin2
	other.signIn(t)
	code := strings.ReplaceAll(start.UserCode, "-", "")
	want := "code " + code + " is for a sign-in started at " + testSignin + ", not at " + testSignin2 + "; enter it at " + testSignin + "/device"
	for _, path := range []string{"/device", "/device/confirm"} {
		resp, page := other.post(t, path, url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, want) {
			t.Errorf("POST %s%s: %s, want 403 with %q\n%s", testSignin2, path, resp.Status, want, page)
		}
	}
	poll := func(origin string) (int, string) {
		h.clock.Advance(pollInterval)
		return h.apiCallOn(t, origin, http.MethodPost, edgeproto.PathDeviceToken, "", edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode}, nil)
	}
	wantPoll := "this sign-in was started at " + testSignin + ", not at " + testSignin2 + "; finish it there"
	if status, msg := poll(testSignin2); status != http.StatusBadRequest || msg != wantPoll {
		t.Errorf("poll on %s: %d %q, want 400 %q", testSignin2, status, msg, wantPoll)
	}
	if got := h.column(t, `SELECT last_poll_at FROM device_authorizations`); got != "0" {
		t.Errorf("a poll refused on %s recorded last_poll_at %s", testSignin2, got)
	}
	b := h.browser(t)
	b.signIn(t)
	if resp, page := b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve on %s: %s\n%s", testSignin, resp.Status, page)
	}
	if status, msg := poll(testSignin2); status != http.StatusBadRequest || msg != wantPoll {
		t.Errorf("redeem on %s: %d %q, want 400 %q", testSignin2, status, msg, wantPoll)
	}
	if status, msg := poll(testSignin); status != http.StatusOK {
		t.Errorf("redeem on %s: %d %s", testSignin, status, msg)
	}

	h = newPairsHarness(t)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	start = startDevice(t, h)
	other = h.browser(t)
	other.origin = testSignin2
	other.signIn(t)
	if resp, page := other.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}}); resp.StatusCode != http.StatusOK {
		t.Errorf("approve on %s without recording: %s\n%s", testSignin2, resp.Status, page)
	}
}

func TestCanonicalOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://Auth.Example.test":      "https://auth.example.test",
		"https://auth.example.test:443":  "https://auth.example.test",
		"https://auth.example.test:8443": "https://auth.example.test:8443",
		"http://127.0.0.1:443":           "http://127.0.0.1:443",
		"http://localhost:80":            "http://localhost",
		"https://[2001:DB8:0::1]:443":    "https://[2001:db8::1]",
		"https://[2001:db8::1]:8443":     "https://[2001:db8::1]:8443",
	} {
		if got := CanonicalOrigin(in); got != want {
			t.Errorf("CanonicalOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNoticeCap fills the notice list, one retiring pair and fifteen
// retired: the relay starts and the signed ready goes over the wire and
// verifies. One more retired pair is refused before the relay starts.
func TestNoticeCap(t *testing.T) {
	retired := func(n int) []Retired {
		var out []Retired
		for i := range n {
			out = append(out, Retired{SigninOrigin: fmt.Sprintf("https://signin%d.old.example.test", i),
				RelayOrigin: fmt.Sprintf("https://relay%d.old.example.test", i), Replacement: testRelay})
		}
		return out
	}
	configure := func(n int) func(*Config, *fakeProvider) {
		return func(cfg *Config, p *fakeProvider) {
			app := p.app()
			app.ClientID, app.ClientSecret = "test-client-2", "test-secret-2"
			cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
			cfg.Retiring = []Retiring{{RelayOrigin: testRelay2, Replacement: testRelay}}
			cfg.Retired = retired(n)
		}
	}
	h := newHarness(t, configure(edgeproto.MaxRelayOrigins-1))
	cfg := h.svc.RelayConfig(0)
	rl, err := relay.New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = rl.Shutdown(t.Context())
	serverID, nonce := testServerID(t), bytes.Repeat([]byte{7}, edgeproto.NonceSize)
	sig := edgeproto.SignNotices(h.svc.key, serverID, testRelay, nonce, cfg.Notices)
	raw, err := edgeproto.EncodeControl(edgeproto.Ready{ServerID: serverID, State: edgeproto.StateClaimed,
		EdgeKey: h.svc.EdgeKey(), Notices: cfg.Notices, NoticesSignature: sig})
	if err != nil {
		t.Fatalf("encode a ready with %d notices: %v", len(cfg.Notices), err)
	}
	m, err := edgeproto.DecodeControl(raw)
	if err != nil {
		t.Fatal(err)
	}
	ready := m.(edgeproto.Ready)
	if len(ready.Notices) != edgeproto.MaxRelayOrigins ||
		edgeproto.VerifyNotices(h.svc.EdgeKey(), serverID, testRelay, nonce, ready.Notices, ready.NoticesSignature) != nil {
		t.Errorf("decoded ready: %d notices, signature verifies: %v", len(ready.Notices),
			edgeproto.VerifyNotices(h.svc.EdgeKey(), serverID, testRelay, nonce, ready.Notices, ready.NoticesSignature))
	}

	h = newHarness(t, configure(edgeproto.MaxRelayOrigins))
	if _, err = relay.New(t.Context(), h.svc.RelayConfig(0)); err == nil || err.Error() != "relay: 17 notices, limit 16" {
		t.Errorf("17 notices: %v", err)
	}
}
