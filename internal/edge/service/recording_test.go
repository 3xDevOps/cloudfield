package edge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

// column reads one origin column of the edge's database, "" for NULL.
func (h *harness) column(t *testing.T, query string, args ...any) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(h.dataDir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	var v sql.NullString
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v.String
}

// newRecordingHarness is newPairsHarness with RecordOrigins set.
func newRecordingHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, func(cfg *Config, p *fakeProvider) {
		app := p.app()
		app.ClientID, app.ClientSecret = "test-client-2", "test-secret-2"
		cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
		cfg.RecordOrigins = true
	})
	if err := h.svc.StartRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(h.dataDir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// TestOriginsRecordedPerPair signs a browser and a device in on each pair
// of a two-pair edge: each records the sign-in origin it was issued at,
// and keeps it when used at the other pair. A row without one takes the
// pair it is next used at, on the sign-in host or, for a device, through
// the relay host.
func TestOriginsRecordedPerPair(t *testing.T) {
	h := newRecordingHarness(t)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	if got := h.column(t, `SELECT group_concat(relay_origin || ' ' || signin_origin, ', ') FROM (SELECT * FROM origin_pairs ORDER BY relay_origin)`); got != testRelay+" "+testSignin+", "+testRelay2+" "+testSignin2 {
		t.Fatalf("origin pairs recorded at start: %q", got)
	}
	pairs := []struct{ signin, relay, other string }{{testSignin, testRelay, testSignin2}, {testSignin2, testRelay2, testSignin}}
	tokens := map[string]string{}
	for _, p := range pairs {
		b := h.browser(t)
		b.origin = p.signin
		b.signIn(t)
		if got := h.column(t, `SELECT signin_origin FROM web_sessions ORDER BY created_at DESC, rowid DESC LIMIT 1`); got != p.signin {
			t.Errorf("session signed in on %s records %q", p.signin, got)
		}

		var start edgeproto.DeviceStartResponse
		if status, msg := h.apiCallOn(t, p.signin, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "laptop"}, &start); status != http.StatusOK {
			t.Fatalf("device start on %s: %d %s", p.signin, status, msg)
		}
		if got := h.column(t, `SELECT signin_origin FROM device_authorizations`); got != p.signin {
			t.Errorf("device authorization started on %s records %q", p.signin, got)
		}
		b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
		h.clock.Advance(pollInterval)
		var tok edgeproto.DeviceTokenResponse
		if status, msg := h.apiCallOn(t, p.signin, http.MethodPost, edgeproto.PathDeviceToken, "",
			edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode}, &tok); status != http.StatusOK {
			t.Fatalf("device token on %s: %d %s", p.signin, status, msg)
		}
		tokens[p.signin] = tok.Token
		if got := h.column(t, `SELECT signin_origin FROM devices WHERE id = ?`, tok.Device.ID); got != p.signin {
			t.Errorf("device token issued on %s records %q", p.signin, got)
		}
		if status, msg := h.apiCallOn(t, p.other, http.MethodGet, edgeproto.PathServers, tok.Token, nil, nil); status != http.StatusOK {
			t.Fatalf("token of %s on %s: %d %s", p.signin, p.other, status, msg)
		}
		if got := h.column(t, `SELECT signin_origin FROM devices WHERE id = ?`, tok.Device.ID); got != p.signin {
			t.Errorf("device token of %s used on %s records %q", p.signin, p.other, got)
		}

		h.exec(t, `UPDATE web_sessions SET signin_origin = NULL`)
		b.origin = p.other
		b.get(t, "/servers")
		b.origin = p.signin
		if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusOK {
			t.Fatalf("/servers on %s: %s", p.signin, resp.Status)
		}
		if got := h.column(t, `SELECT group_concat(signin_origin) FROM web_sessions`); got != p.signin {
			t.Errorf("sessions without an origin, one used on %s and on %s: origins %q, want only %s", p.other, p.signin, got, p.signin)
		}
	}

	h.exec(t, `UPDATE devices SET signin_origin = NULL`)
	if status, msg := h.apiCallOn(t, testSignin2, http.MethodGet, edgeproto.PathServers, tokens[testSignin], nil, nil); status != http.StatusOK {
		t.Fatalf("token on %s: %d %s", testSignin2, status, msg)
	}
	relay := relayDirectory{h.svc}
	ctx := context.WithValue(context.Background(), pairKey{}, h.svc.byHost[edgeproto.OriginHost(testRelay)])
	if _, _, err := relay.Authenticate(ctx, tokens[testSignin2]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := relay.Authenticate(context.Background(), tokens[testSignin]); err != nil {
		t.Fatal(err)
	}
	got := h.column(t, `SELECT group_concat(signin_origin || ':' || signin_origin_seen, ' ') FROM (SELECT * FROM devices ORDER BY created_at, id)`)
	if got != testSignin2+":1 "+testSignin+":1" {
		t.Errorf("devices without an origin next used on %s and through %s record %q, want %s then %s", testSignin2, testRelay, got, testSignin2, testSignin)
	}
}

// TestEnrollmentRecordsOrigin enrolls a claimed server at each relay
// origin: the last enrollment's origin and build are what the store
// holds.
func TestEnrollmentRecordsOrigin(t *testing.T) {
	h := newRecordingHarness(t)
	id := testServerID(t)
	h.claim(t, id, "workstation", edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "owner"})
	if got := h.column(t, `SELECT relay_origin || ' ' || agent_version FROM servers WHERE id = ?`, id); got != testRelay+" v1.0.0" {
		t.Fatalf("claimed server records %q", got)
	}
	relay := relayDirectory{h.svc}
	for _, e := range []struct{ origin, build string }{{testRelay2, "v2.0.0"}, {testRelay, "v2.1.0"}} {
		if claimed, err := relay.Enroll(context.Background(), id, "workstation", edgeproto.PolicyAccount, e.origin, e.build); err != nil || !claimed {
			t.Fatalf("enroll at %s: %v %v", e.origin, claimed, err)
		}
		if got := h.column(t, `SELECT relay_origin || ' ' || agent_version FROM servers WHERE id = ?`, id); got != e.origin+" "+e.build {
			t.Errorf("enrolled at %s with %s, records %q", e.origin, e.build, got)
		}
	}
	if err := h.svc.RelayConfig(0).Connectors.ConnectorUsed(context.Background(), id, testRelay2); err != nil {
		t.Fatal(err)
	}
	if got := h.column(t, `SELECT relay_origin FROM connector_use WHERE server_id = ?`, id); got != testRelay2 {
		t.Errorf("connector use records %q", got)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := h.column(t, `SELECT stopped_at = ? FROM origin_recording`, h.clock.Now().UnixNano()); got != "1" {
		t.Errorf("a recording edge's close did not record when it stopped")
	}
}

// TestOriginsNotRecordedByDefault runs every writer on a two-pair edge
// without RecordOrigins: nothing that schema 2 adds is written.
func TestOriginsNotRecordedByDefault(t *testing.T) {
	h := newPairsHarness(t)
	h.exec(t, `INSERT INTO origin_recording (id, since, started_at, stopped_at) VALUES (1, 1, 2, 3)`)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	b := h.browser(t)
	b.origin = testSignin2
	b.signIn(t)
	var start edgeproto.DeviceStartResponse
	if status, msg := h.apiCallOn(t, testSignin2, http.MethodPost, edgeproto.PathDeviceStart, "",
		edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "laptop"}, &start); status != http.StatusOK {
		t.Fatalf("device start: %d %s", status, msg)
	}
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	h.clock.Advance(pollInterval)
	var tok edgeproto.DeviceTokenResponse
	if status, msg := h.apiCallOn(t, testSignin2, http.MethodPost, edgeproto.PathDeviceToken, "",
		edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode}, &tok); status != http.StatusOK {
		t.Fatalf("device token: %d %s", status, msg)
	}
	h.apiCallOn(t, testSignin2, http.MethodGet, edgeproto.PathServers, tok.Token, nil, nil)
	if status, msg := h.apiCallOn(t, testSignin2, http.MethodPost, edgeproto.PathDeviceStart, "",
		edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "phone"}, &start); status != http.StatusOK {
		t.Fatalf("second device start: %d %s", status, msg)
	}
	b.get(t, "/servers")
	relay := relayDirectory{h.svc}
	ctx := context.WithValue(context.Background(), pairKey{}, h.svc.byHost[edgeproto.OriginHost(testRelay2)])
	if _, _, err := relay.Authenticate(ctx, tok.Token); err != nil {
		t.Fatal(err)
	}
	id := testServerID(t)
	h.claim(t, id, "workstation", edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "owner"})
	if _, err := relay.Enroll(ctx, id, "workstation", edgeproto.PolicyAccount, testRelay2, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if c := h.svc.RelayConfig(0).Connectors; c != nil {
		t.Errorf("relay configuration without RecordOrigins records connectors with %T", c)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM origin_pairs`,
		`SELECT COUNT(*) FROM device_authorizations WHERE signin_origin IS NOT NULL`,
		`SELECT COUNT(*) FROM connector_use`,
		`SELECT COUNT(*) FROM servers WHERE relay_origin IS NOT NULL OR agent_version IS NOT NULL OR enrolled_at IS NOT NULL`,
		`SELECT COUNT(*) FROM devices WHERE signin_origin IS NOT NULL OR signin_origin_seen IS NOT NULL`,
		`SELECT COUNT(*) FROM web_sessions WHERE signin_origin IS NOT NULL OR signin_origin_seen IS NOT NULL`,
	} {
		if got := h.column(t, q); got != "0" {
			t.Errorf("%s = %s without RecordOrigins, want 0", q, got)
		}
	}
	if got := h.column(t, `SELECT COUNT(*) FROM devices`) + " " + h.column(t, `SELECT COUNT(*) FROM device_authorizations`); got != "1 1" {
		t.Fatalf("devices and pending sign-ins: %s, want the one signed in and the one pending", got)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := h.column(t, `SELECT since || ' ' || started_at || ' ' || stopped_at FROM origin_recording`); got != "1 2 3" {
		t.Fatalf("an edge that does not record changed the recorded period to %s", got)
	}
}

// TestRedemptionAfterRecordingStops starts a device sign-in while the edge
// records origins and redeems it once it no longer does, on the same
// database: the device records no origin.
func TestRedemptionAfterRecordingStops(t *testing.T) {
	h := newRecordingHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	if got := h.column(t, `SELECT signin_origin FROM device_authorizations`); got != testSignin {
		t.Fatalf("authorization started while recording: %q", got)
	}
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	h.clock.Advance(pollInterval)
	restarted, err := New(Config{DataDir: h.dataDir, SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: h.provider.app(), Clock: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() }) //nolint:errcheck // test cleanup
	restarted.SetLink(h.link)
	body, err := json.Marshal(edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, testSignin+edgeproto.PathDeviceToken, bytes.NewReader(body))
	req.Header.Set(edgeproto.HeaderVersion, fmt.Sprint(edgeproto.Version))
	rec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(rec, req)
	var tok edgeproto.DeviceTokenResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &tok) != nil {
		t.Fatalf("poll after the restart: %d %s", rec.Code, rec.Body)
	}
	if got := h.column(t, `SELECT signin_origin FROM devices WHERE id = ?`, tok.Device.ID); got != "" {
		t.Fatalf("device redeemed after recording stopped records %q", got)
	}
}

// TestRedemptionAfterRecordingStarts starts a device sign-in while the
// edge does not record origins and polls it once it does, on the same
// database: the sign-in, bound to no pair, was dropped at the start and
// is started again.
func TestRedemptionAfterRecordingStarts(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	h.clock.Advance(pollInterval)
	restarted, err := New(Config{DataDir: h.dataDir, SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: h.provider.app(),
		Clock: h.clock.Now, RecordOrigins: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() }) //nolint:errcheck // test cleanup
	restarted.SetLink(h.link)
	if err = restarted.StartRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, testSignin+edgeproto.PathDeviceToken, bytes.NewReader(body))
	req.Header.Set(edgeproto.HeaderVersion, fmt.Sprint(edgeproto.Version))
	rec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(edgeproto.DeviceExpired)) {
		t.Fatalf("poll after the restart: %d %s, want 400 %s", rec.Code, rec.Body, edgeproto.DeviceExpired)
	}
}

// TestRecordingStartsWhenServing opens a recording edge that never starts
// serving, as when its listener fails to bind: closing it records no
// pairs and no period.
func TestRecordingStartsWhenServing(t *testing.T) {
	dir := t.TempDir()
	svc, err := New(Config{DataDir: dir, SigninOrigin: testSignin, RelayOrigin: testRelay,
		GitHub: &OAuthApp{ClientID: "id", ClientSecret: "secret"}, RecordOrigins: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.signinOrigin(context.WithValue(context.Background(), pairKey{}, svc.pairs[0])); got != "" {
		t.Errorf("sign-in origin recorded before the run started: %q", got)
	}
	if e := svc.enrollment(testRelay, "v1.0.0"); e != (edgestore.Enrollment{}) {
		t.Errorf("enrollment recorded before the run started: %+v", e)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	h := &harness{dataDir: dir}
	for _, table := range []string{"origin_pairs", "origin_recording"} {
		if got := h.column(t, `SELECT COUNT(*) FROM `+table); got != "0" {
			t.Errorf("%s holds %s rows after a start that never served", table, got)
		}
	}
}

// TestDeviceAuthAcrossRestart starts a device sign-in, then restarts the
// edge: with its sign-in origin paired with another relay origin the poll
// is refused; with the same pair, also spelled with its default port, it
// goes on. A pending sign-in bound to no relay origin is dropped when a
// start records origins.
func TestDeviceAuthAcrossRestart(t *testing.T) {
	for _, tt := range []struct {
		relay   string
		refused bool
	}{
		{"https://relay9.example.test", true},
		{testRelay, false},
		{testRelay + ":443", false},
		{testRelay + ":0443", false},
	} {
		h := newRecordingHarness(t)
		start := startDevice(t, h)
		if err := h.svc.Close(); err != nil {
			t.Fatal(err)
		}
		svc, err := New(Config{DataDir: h.dataDir, SigninOrigin: testSignin, RelayOrigin: tt.relay, GitHub: h.provider.app(), Clock: h.clock.Now, RecordOrigins: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		svc.SetLink(&fakeLink{})
		if err = svc.StartRecording(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(pollInterval)
		body, _ := json.Marshal(edgeproto.DeviceTokenRequest{DeviceCode: start.DeviceCode})
		req := httptest.NewRequest(http.MethodPost, testSignin+edgeproto.PathDeviceToken, bytes.NewReader(body))
		req.Header.Set(edgeproto.HeaderVersion, fmt.Sprint(edgeproto.Version))
		rec := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rec, req)
		if refused := strings.Contains(rec.Body.String(), "this sign-in was started at"); refused != tt.refused {
			t.Errorf("poll after a restart with the relay origin %s: %d %s, want refused %v", tt.relay, rec.Code, rec.Body, tt.refused)
		}
	}

	h := newRecordingHarness(t)
	h.exec(t, `INSERT INTO device_authorizations (code_hash, user_code_hash, label, public_key, client_addr, status, created_at, expires_at, last_poll_at, signin_origin)
		VALUES ('legacy', 'user-legacy', 'laptop', 'key', '127.0.0.1', 'pending', 0, 9000000000000000000, 0, ?)`, testSignin)
	if err := h.svc.StartRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.column(t, `SELECT COUNT(*) FROM device_authorizations`); got != "0" {
		t.Errorf("%s device authorizations bound to no relay origin survived a start that records origins", got)
	}
}

// TestDeviceAuthWithoutRelayOrigin polls, on an edge that does not record
// origins, a pending sign-in that records only its sign-in origin, as one
// started while the edge recorded origins before the step that added the
// relay origin: it goes on at its sign-in origin and is refused at the
// other.
func TestDeviceAuthWithoutRelayOrigin(t *testing.T) {
	h := newPairsHarness(t)
	code := edgeproto.NewToken()
	h.exec(t, `INSERT INTO device_authorizations (code_hash, user_code_hash, label, public_key, client_addr, status, created_at, expires_at, last_poll_at, signin_origin)
		VALUES (?, 'user-code', 'laptop', 'key', '127.0.0.1', 'pending', 0, 9000000000000000000, 0, ?)`, edgeproto.HashToken(code), testSignin)
	poll := func(origin string) (int, string) {
		h.clock.Advance(pollInterval)
		return h.apiCallOn(t, origin, http.MethodPost, edgeproto.PathDeviceToken, "", edgeproto.DeviceTokenRequest{DeviceCode: code}, nil)
	}
	if status, msg := poll(testSignin); msg != string(edgeproto.DevicePending) {
		t.Errorf("poll at %s: %d %q, want %s", testSignin, status, msg, edgeproto.DevicePending)
	}
	want := "this sign-in was started at " + testSignin + ", not at " + testSignin2 + " for " + testRelay2 + "; finish it there"
	if status, msg := poll(testSignin2); status != http.StatusBadRequest || msg != want {
		t.Errorf("poll at %s: %d %q, want 400 %q", testSignin2, status, msg, want)
	}
}

// TestDeviceAuthRecordsCanonicalRelay starts a device sign-in on a pair
// whose relay origin is written with its default port: the sign-in
// records the relay origin without it.
func TestDeviceAuthRecordsCanonicalRelay(t *testing.T) {
	h := newHarness(t, func(cfg *Config, _ *fakeProvider) {
		cfg.RelayOrigin, cfg.RecordOrigins = testRelay+":443", true
	})
	if err := h.svc.StartRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
	startDevice(t, h)
	if got := h.column(t, `SELECT relay_origin FROM device_authorizations`); got != testRelay {
		t.Errorf("relay_origin = %q, want %s", got, testRelay)
	}
}
