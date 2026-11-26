package edge

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
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
	h := newPairsHarness(t)
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
	got := h.column(t, `SELECT group_concat(signin_origin, ' ') FROM (SELECT signin_origin FROM devices ORDER BY created_at, id)`)
	if got != testSignin2+" "+testSignin {
		t.Errorf("devices without an origin next used on %s and through %s record %q, want %s then %s", testSignin2, testRelay, got, testSignin2, testSignin)
	}
}

// TestEnrollmentRecordsOrigin enrolls a claimed server at each relay
// origin: the last enrollment's origin and build are what the store
// holds.
func TestEnrollmentRecordsOrigin(t *testing.T) {
	h := newPairsHarness(t)
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
	if err := relay.ConnectorUsed(context.Background(), id, testRelay2); err != nil {
		t.Fatal(err)
	}
	if got := h.column(t, `SELECT relay_origin FROM connector_use WHERE server_id = ?`, id); got != testRelay2 {
		t.Errorf("connector use records %q", got)
	}
}
