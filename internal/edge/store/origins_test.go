package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

const (
	testSignin2 = "https://signin2.example.test"
	testRelay2  = "https://relay2.example.test"
)

func nullString(t *testing.T, s *Store, query string, args ...any) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMigrationFromVersion1 migrates a database with an account, a
// device, a session and a claimed server: the rows stay, their new
// columns are empty, and an older binary still runs on the result.
func TestMigrationFromVersion1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	old := openSteps(t, path, migrations[:1])
	for _, q := range []string{
		`INSERT INTO accounts VALUES (1, 'acc', 'github', '1', 'owner@example.test', 'owner', 'Owner', 0, 0)`,
		`INSERT INTO devices VALUES ('dev', 'token-hash', 1, 'ssh-ed25519 AAAA', 'laptop', 0, 5)`,
		`INSERT INTO web_sessions VALUES ('sess', 'session-hash', 1, 0, 9000000000000000000)`,
		`INSERT INTO servers VALUES ('` + serverA + `', 'alpha', 'self-hosted', 'account', 'account', 1, 0)`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	closeStore(t, old)

	s := openSteps(t, path, migrations)
	if got, err := ReadSchema(path); err != nil || got != (Schema{Version: 2, CompatFloor: 1}) {
		t.Fatalf("schema = %+v, %v; want version 2 at floor 1", got, err)
	}
	if names := backupsIn(t, dir); len(names) != 1 || !strings.HasPrefix(names[0], "edge-v1-v2-") {
		t.Fatalf("backups = %v, want one taken before migrating 1 to 2", names)
	}
	for _, q := range []string{
		`SELECT signin_origin FROM devices WHERE id = 'dev'`,
		`SELECT signin_origin FROM web_sessions WHERE id = 'sess'`,
		`SELECT relay_origin FROM servers WHERE id = '` + serverA + `'`,
		`SELECT agent_version FROM servers WHERE id = '` + serverA + `'`,
	} {
		if v := nullString(t, s, q); v.Valid {
			t.Errorf("%s = %q after the migration, want NULL", q, v.String)
		}
	}
	d, err := s.UseDevice(context.Background(), "token-hash", "", testNow)
	if err != nil || d.Label != "laptop" || d.SigninOrigin != "" {
		t.Fatalf("device after the migration = %+v, %v", d, err)
	}
	closeStore(t, s)

	older := openSteps(t, path, migrations[:1])
	defer closeStore(t, older)
	if _, err := older.db.Exec(`DELETE FROM servers WHERE id = ?`, serverA); err != nil {
		t.Fatalf("the older binary deleting a server: %v", err)
	}
}

// TestOriginsBackupAndRestore checks that a backup holds what schema 2
// adds and a restore brings it back.
func TestOriginsBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	s := openSteps(t, path, migrations)
	ctx := context.Background()
	claim(t, s, serverA, "alpha", owner())
	if err := s.UseConnector(ctx, serverA, testRelay2, testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPairs(ctx, map[string]string{testRelay: testSignin}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin)
		VALUES ('dev', 'token-hash', 1, 'ssh-ed25519 AAAA', 'laptop', 0, 0, ?)`, testSignin); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	backup := filepath.Join(dir, "copy.db")
	if _, err := Backup(path, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "edge.db")
	staged, _, err := StageRestore(backup, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = FinishRestore(staged, target); err != nil {
		t.Fatal(err)
	}
	r := openSteps(t, target, migrations)
	defer closeStore(t, r)
	for q, want := range map[string]string{
		`SELECT relay_origin FROM servers`:              testRelay,
		`SELECT agent_version FROM servers`:             "v1.0.0",
		`SELECT relay_origin FROM connector_use`:        testRelay2,
		`SELECT signin_origin FROM origin_pairs`:        testSignin,
		`SELECT signin_origin FROM devices`:             testSignin,
		`SELECT CAST(enrolled_at AS TEXT) FROM servers`: strconv.FormatInt(unix(testNow), 10),
	} {
		if got := nullString(t, r, q); got.String != want {
			t.Errorf("restored %s = %q, want %q", q, got.String, want)
		}
	}
}

// TestOriginWriters checks each writer: enrollment and claim record the
// relay origin, time and build; a device token and a session record the
// sign-in origin at issue, and one without it at next use only.
func TestOriginWriters(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	me := owner()
	id, err := s.SignIn(ctx, me, testNow)
	if err != nil {
		t.Fatal(err)
	}
	claim(t, s, serverA, "alpha", me)
	later := testNow.Add(time.Hour)
	if claimed, err := s.EnrollServer(ctx, serverA, "alpha", edgeproto.PolicyAccount,
		Enrollment{RelayOrigin: testRelay2, AgentVersion: "v2.0.0", At: later}); err != nil || !claimed {
		t.Fatalf("enroll = %v, %v", claimed, err)
	}
	if claimed, err := s.EnrollServer(ctx, serverB, "beta", edgeproto.PolicyAccount, testEnrollment); err != nil || claimed {
		t.Fatalf("enroll an unclaimed server = %v, %v", claimed, err)
	}
	var origin, build string
	var at int64
	if err := s.db.QueryRow(`SELECT relay_origin, agent_version, enrolled_at FROM servers WHERE id = ?`, serverA).
		Scan(&origin, &build, &at); err != nil || origin != testRelay2 || build != "v2.0.0" || !fromUnix(at).Equal(later) {
		t.Fatalf("server after enrolling = %s %s %v, %v", origin, build, fromUnix(at), err)
	}
	if n := countRows(t, s, "servers"); n != 1 {
		t.Fatalf("enrolling an unclaimed server created a row: %d servers", n)
	}
	if _, err := s.EnrollServer(ctx, serverA, "renamed", edgeproto.PolicyAccount, Enrollment{}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT relay_origin, agent_version, enrolled_at FROM servers WHERE id = ?`, serverA).
		Scan(&origin, &build, &at); err != nil || origin != testRelay2 || build != "v2.0.0" || !fromUnix(at).Equal(later) {
		t.Fatalf("server after enrolling with nothing to record = %s %s %v, %v", origin, build, fromUnix(at), err)
	}
	claim(t, s, serverB, "beta", me)
	if err := s.DropOwner(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaim(ctx, serverB, "beta", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(me), Enrollment{}, later); err != nil {
		t.Fatal(err)
	}
	if got := nullString(t, s, `SELECT relay_origin || ' ' || agent_version FROM servers WHERE id = ?`, serverB); got.String != testRelay+" v1.0.0" {
		t.Fatalf("server claimed again with nothing to record = %q, want its earlier enrollment kept", got.String)
	}

	addDevice(t, s, id, "issued", "issued-hash")
	if _, err := s.db.Exec(`UPDATE devices SET signin_origin = ? WHERE id = 'issued'`, testSignin2); err != nil {
		t.Fatal(err)
	}
	auth := DeviceAuth{CodeHash: "code-new", UserCodeHash: "user-new", Label: "phone", Key: "ssh-ed25519 BBBB",
		CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour), SigninOrigin: testSignin}
	if err := s.CreateDeviceAuth(ctx, auth); err != nil {
		t.Fatal(err)
	}
	if a, err := s.PendingDeviceAuth(ctx, auth.UserCodeHash, testNow); err != nil || a.SigninOrigin != testSignin {
		t.Fatalf("pending authorization = %+v, %v; want its origin", a, err)
	}
	if a, err := s.PollDeviceAuth(ctx, auth.CodeHash, testNow); err != nil || a.SigninOrigin != testSignin {
		t.Fatalf("polled authorization = %+v, %v; want its origin", a, err)
	}
	if err := s.DecideDeviceAuth(ctx, auth.UserCodeHash, id, true, testNow); err != nil {
		t.Fatal(err)
	}
	if got := nullString(t, s, `SELECT signin_origin FROM device_authorizations WHERE code_hash = 'code-new'`); got.String != testSignin {
		t.Fatalf("device authorization started at %s records %q", testSignin, got.String)
	}
	if d, err := s.RedeemDeviceAuth(ctx, auth.CodeHash, Device{ID: "new", TokenHash: "new-hash"}, true, testNow); err != nil || d.SigninOrigin != testSignin {
		t.Fatalf("redeemed device = %+v, %v; want the authorization's origin", d, err)
	}
	off := DeviceAuth{CodeHash: "code-off", UserCodeHash: "user-off", Label: "tablet", Key: "ssh-ed25519 DDDD",
		CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour), SigninOrigin: testSignin}
	if err := s.CreateDeviceAuth(ctx, off); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceAuth(ctx, off.UserCodeHash, id, true, testNow); err != nil {
		t.Fatal(err)
	}
	if d, err := s.RedeemDeviceAuth(ctx, off.CodeHash, Device{ID: "off", TokenHash: "off-hash"}, false, testNow); err != nil || d.SigninOrigin != "" {
		t.Fatalf("device redeemed with recording off = %+v, %v; want no origin", d, err)
	}
	if got := nullString(t, s, `SELECT signin_origin FROM devices WHERE id = 'off'`); got.Valid {
		t.Fatalf("device redeemed with recording off records %q", got.String)
	}
	if _, err := s.db.Exec(`DELETE FROM devices WHERE id = 'off'`); err != nil {
		t.Fatal(err)
	}
	before := DeviceAuth{CodeHash: "code-before", UserCodeHash: "user-before", Label: "tablet", Key: "ssh-ed25519 EEEE",
		CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour)}
	if err := s.CreateDeviceAuth(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceAuth(ctx, before.UserCodeHash, id, true, testNow); err != nil {
		t.Fatal(err)
	}
	if d, err := s.RedeemDeviceAuth(ctx, before.CodeHash, Device{ID: "before", TokenHash: "before-hash", SigninOrigin: testSignin2}, true, testNow); err != nil || d.SigninOrigin != testSignin2 {
		t.Fatalf("device redeemed once recording began = %+v, %v; want the redemption's origin", d, err)
	}
	if got := nullString(t, s, `SELECT signin_origin || ' ' || signin_origin_seen FROM devices WHERE id = 'before'`); got.String != testSignin2+" 1" {
		t.Fatalf("device redeemed once recording began records %q, want %s seen", got.String, testSignin2)
	}
	if _, err := s.db.Exec(`DELETE FROM devices WHERE id = 'before'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at)
		VALUES ('bare', 'bare-hash', ?, 'ssh-ed25519 CCCC', 'old', 0, 0)`, id); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		hash, signin, want string
	}{
		{"new-hash", testSignin2, testSignin},
		{"issued-hash", testSignin, testSignin2},
		{"bare-hash", "", ""},
		{"bare-hash", testSignin2, testSignin2},
		{"bare-hash", testSignin, testSignin2},
	} {
		d, err := s.UseDevice(ctx, tt.hash, tt.signin, later)
		if err != nil || d.SigninOrigin != tt.want || !d.LastUsedAt.Equal(later) {
			t.Errorf("use %s at %q = %q last used %v, %v; want %q", tt.hash, tt.signin, d.SigninOrigin, d.LastUsedAt, err, tt.want)
		}
	}
	if got := nullString(t, s, `SELECT group_concat(id || ':' || COALESCE(signin_origin_seen, 'issued'), ' ') FROM (SELECT * FROM devices ORDER BY id)`); got.String != "bare:1 issued:issued new:issued" {
		t.Errorf("devices issued and seen: %q", got.String)
	}

	if err := s.CreateSession(ctx, "issued", "session-issued", id, testSignin2, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "bare", "session-bare", id, "", testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		hash, signin, want string
	}{
		{"session-issued", testSignin, testSignin2},
		{"session-bare", "", ""},
		{"session-bare", testSignin, testSignin},
		{"session-bare", testSignin2, testSignin},
	} {
		sess, err := s.UseSession(ctx, tt.hash, tt.signin, testNow, testNow.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if got := nullString(t, s, `SELECT signin_origin FROM web_sessions WHERE id = ?`, sess.ID); got.String != tt.want {
			t.Errorf("use session %s at %q: origin %q, want %q", tt.hash, tt.signin, got.String, tt.want)
		}
	}
	if got := nullString(t, s, `SELECT group_concat(id || ':' || COALESCE(signin_origin_seen, 'issued'), ' ') FROM (SELECT * FROM web_sessions ORDER BY id)`); got.String != "bare:1 issued:issued" {
		t.Errorf("sessions issued and seen: %q", got.String)
	}
}

// TestUseConnector records a connector request for a claimed server only,
// once per server and relay origin, keeping the latest.
func TestUseConnector(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	claim(t, s, serverA, "alpha", owner())
	for _, u := range []struct {
		id, origin string
		at         time.Time
	}{
		{serverA, testRelay, testNow},
		{serverA, testRelay, testNow.Add(time.Hour)},
		{serverA, testRelay2, testNow},
		{serverB, testRelay, testNow},
	} {
		if err := s.UseConnector(ctx, u.id, u.origin, u.at); err != nil {
			t.Fatalf("UseConnector(%s, %s): %v", u.id, u.origin, err)
		}
	}
	rows, err := s.db.Query(`SELECT server_id, relay_origin, last_used_at FROM connector_use ORDER BY relay_origin`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, origin string
		var at int64
		if err := rows.Scan(&id, &origin, &at); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+" "+origin+" "+fromUnix(at).Format(time.RFC3339))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	want := []string{serverA + " " + testRelay + " 2026-09-27T13:00:00Z", serverA + " " + testRelay2 + " 2026-09-27T12:00:00Z"}
	if !slices.Equal(got, want) {
		t.Fatalf("connector_use = %v, want %v", got, want)
	}
}

// TestOriginRowsGoWithTheirParent checks that what schema 2 adds leaves
// with the server, device or account it belongs to.
func TestOriginRowsGoWithTheirParent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	me := owner()
	id, err := s.SignIn(ctx, me, testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{serverA, serverB} {
		claim(t, s, sid, sid[:5], me)
		if err := s.UseConnector(ctx, sid, testRelay, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "connector_use"); n != 1 {
		t.Fatalf("connector_use after removing a server: %d rows, want 1", n)
	}
	if err := s.BlockServer(ctx, serverB, testNow); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "connector_use"); n != 0 {
		t.Fatalf("connector_use after blocking the other: %d rows, want 0", n)
	}

	addDevice(t, s, id, "one", "one-hash")
	addDevice(t, s, id, "two", "two-hash")
	if _, err := s.UseDevice(ctx, "one-hash", testSignin, testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, id, "one"); err != nil {
		t.Fatal(err)
	}
	if n := nullString(t, s, `SELECT COUNT(*) FROM devices WHERE signin_origin IS NOT NULL`); n.String != "0" {
		t.Fatalf("devices with an origin after deleting the one: %s", n.String)
	}
	if err := s.CreateSession(ctx, "sess", "sess-hash", id, testSignin, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAccount(ctx, me.Provider, me.Subject, testNow); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"devices", "web_sessions"} {
		if n := countRows(t, s, table); n != 0 {
			t.Errorf("%s after deleting the account: %d rows", table, n)
		}
	}
}

// TestOriginWritesUseIndexes checks the plans of the statements that
// record origins on every device, session, enrollment and connector use.
func TestOriginWritesUseIndexes(t *testing.T) {
	s := openStore(t)
	for _, q := range []string{useDeviceQuery, useSessionQuery, enrollQuery, useConnectorQuery} {
		args := make([]any, strings.Count(q, "?"))
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, step := range plan {
			if strings.HasPrefix(step, "SCAN ") {
				t.Errorf("plan of %q scans a table: %q", q, strings.Join(plan, "; "))
				break
			}
		}
	}
}

// TestRetiringReport reports an empty store, then two pairs: a server that
// moved shows at its new relay origin only, each count falls in the
// windows its last use is within, and what has no origin is unknown.
func TestRetiringReport(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if got, err := s.Retiring(ctx, testNow); err != nil || !got.RecordingSince.IsZero() || got.Origins == nil || len(got.Origins) != 0 || got.Unknown != (Unknown{}) {
		t.Fatalf("report of an empty store = %+v, %v", got, err)
	}
	if err := s.RecordPairs(ctx, map[string]string{testRelay: testSignin}, testNow.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Retiring(ctx, testNow)
	if err != nil || len(got.Origins) != 1 || got.Origins[0].RelayOrigin != testRelay || got.Origins[0].SigninOrigin != testSignin ||
		got.Origins[0].Servers == nil || len(got.Origins[0].Servers) != 0 || got.Origins[0].DeviceTokens != (Counts{}) {
		t.Fatalf("report of one pair with nobody on it = %+v, %v", got, err)
	}
	if err = s.StopRecording(ctx, testNow.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPairs(ctx, map[string]string{testRelay: testSignin, testRelay2: testSignin2}, testNow); err != nil {
		t.Fatal(err)
	}

	me, other := owner(), edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	claim(t, s, serverA, "alpha", me)
	claim(t, s, serverB, "beta", other)
	if _, err = s.EnrollServer(ctx, serverA, "alpha", edgeproto.PolicyAccount,
		Enrollment{RelayOrigin: testRelay2, AgentVersion: "v2.0.0", At: testNow}); err != nil {
		t.Fatal(err)
	}
	if err = s.DropOwner(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordClaim(ctx, "cccccccccccccccccccccccccc", "gamma", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(me), Enrollment{}, testNow); err != nil {
		t.Fatal(err)
	}
	myID, _ := s.SignIn(ctx, me, testNow)
	otherID, _ := s.SignIn(ctx, other, testNow)
	day := 24 * time.Hour
	for _, d := range []struct {
		id      string
		account int64
		origin  any
		seen    any
		age     time.Duration
	}{
		{"d1", myID, testSignin, nil, time.Hour},
		{"d2", myID, testSignin, 1, 2 * day},
		{"d3", myID, testSignin2, nil, 0},
		{"d4", otherID, testSignin, nil, 10 * day},
		{"d5", otherID, testSignin, nil, 40 * day},
		{"d6", otherID, nil, nil, 0},
		{"d7", otherID, nil, nil, 8 * day},
	} {
		if _, err = s.db.Exec(`INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin, signin_origin_seen)
			VALUES (?, ?, ?, 'ssh-ed25519 AAAA', 'laptop', 0, ?, ?, ?)`,
			d.id, d.id+"-hash", d.account, unix(testNow.Add(-d.age)), d.origin, d.seen); err != nil {
			t.Fatal(err)
		}
	}
	for _, sess := range []struct {
		id      string
		account int64
		origin  string
		age     time.Duration
	}{
		{"s1", myID, testSignin, 3 * day},
		{"s2", myID, testSignin, 20 * day},
		{"s4", otherID, testSignin2, 0},
		{"s5", otherID, "", 2 * day},
		{"s3", otherID, testSignin, 31 * day},
	} {
		used := testNow.Add(-sess.age)
		if err = s.CreateSession(ctx, sess.id, sess.id+"-hash", sess.account, sess.origin, used, used.Add(edgeproto.SessionIdle)); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []struct {
		id, origin string
		age        time.Duration
	}{
		{serverA, testRelay, 5 * day},
		{serverA, testRelay2, 0},
		{serverB, testRelay, 29 * day},
	} {
		if err = s.UseConnector(ctx, u.id, u.origin, testNow.Add(-u.age)); err != nil {
			t.Fatal(err)
		}
	}

	if n := countRows(t, s, "web_sessions"); n != 5 {
		t.Fatalf("%d sessions, want the expired one kept for the report to leave out", n)
	}
	got, err = s.Retiring(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RecordingSince.Equal(testNow.Add(-90*day)) || got.Unknown != (Unknown{
		DeviceTokens: Counts{Day: 1, Week: 1, Month: 2, Total: 2}, SessionAccounts: Counts{Day: 0, Week: 1, Month: 1, Total: 1}, Servers: 1,
	}) {
		t.Errorf("recording since %v, unknown %+v", got.RecordingSince, got.Unknown)
	}
	want := []OriginReport{{
		RelayOrigin: testRelay, SigninOrigin: testSignin,
		Servers: []SeenServer{{ID: serverB, Name: "beta", AgentVersion: "v1.0.0", LastSeenAt: testNow}},
		SigninCounts: SigninCounts{
			DeviceTokens:       Counts{Day: 1, Week: 1, Month: 2, Total: 3},
			DeviceTokensSeen:   Counts{Day: 0, Week: 1, Month: 1, Total: 1},
			SessionAccounts:    Counts{Day: 0, Week: 1, Month: 1, Total: 1},
			DeviceOnlyAccounts: Counts{Day: 0, Week: 0, Month: 1, Total: 1},
		},
		ConnectorServers: Counts{Day: 0, Week: 1, Month: 2, Total: 2},
	}, {
		RelayOrigin: testRelay2, SigninOrigin: testSignin2,
		Servers: []SeenServer{{ID: serverA, Name: "alpha", OwnerLogin: "Owner", AgentVersion: "v2.0.0", LastSeenAt: testNow}},
		SigninCounts: SigninCounts{
			DeviceTokens:    Counts{Day: 1, Week: 1, Month: 1, Total: 1},
			SessionAccounts: Counts{Day: 1, Week: 1, Month: 1, Total: 1},
		},
		ConnectorServers: Counts{Day: 1, Week: 1, Month: 1, Total: 1},
	}}
	if len(got.Origins) != len(want) {
		t.Fatalf("reports = %+v, want %+v", got.Origins, want)
	}
	for i := range want {
		if g, w := got.Origins[i], want[i]; !slices.Equal(g.Servers, w.Servers) || g.RelayOrigin != w.RelayOrigin || g.SigninOrigin != w.SigninOrigin ||
			g.SigninCountedUnder != "" || g.SigninCounts != w.SigninCounts || g.ConnectorServers != w.ConnectorServers {
			t.Errorf("report = %+v\nwant     %+v", g, w)
		}
	}
}

// TestDeleteSignIns deletes, in one call, the device tokens, sessions and
// pending authorizations of the given origins and those with none, and
// counts what has none.
func TestDeleteSignIns(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.SignIn(ctx, owner(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []any{testSignin, testSignin2, nil, nil} {
		n := countRows(t, s, "devices")
		if _, err := s.db.Exec(`INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin)
			VALUES (?, ?, ?, 'k', 'laptop', 0, 0, ?)`, n, n, id, origin); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateSession(ctx, strconv.Itoa(n), strconv.Itoa(n), id, fmt.Sprint(origin), testNow, testNow.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateDeviceAuth(ctx, DeviceAuth{CodeHash: strconv.Itoa(n), UserCodeHash: strconv.Itoa(n), Label: "l", Key: "k",
			CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour), SigninOrigin: fmt.Sprint(origin)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE web_sessions SET signin_origin = NULL WHERE signin_origin = '<nil>';
		UPDATE device_authorizations SET signin_origin = NULL WHERE signin_origin = '<nil>'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SignInsWithoutOrigin(ctx); err != nil || got != (SignIns{Devices: 2, Sessions: 2, Authorizations: 2}) {
		t.Fatalf("without an origin: %+v, %v", got, err)
	}
	if got, err := s.DeleteSignIns(ctx, []string{testSignin2}); err != nil || got != (SignIns{Devices: 3, Sessions: 3, Authorizations: 3}) {
		t.Fatalf("deleted %+v, %v; want the three of each at %s or with no origin", got, err, testSignin2)
	}
	for _, table := range []string{"devices", "web_sessions", "device_authorizations"} {
		if got := nullString(t, s, `SELECT group_concat(signin_origin) FROM `+table); got.String != testSignin {
			t.Errorf("%s left: %q, want only %s", table, got.String, testSignin)
		}
	}
	if got, err := s.DeleteSignIns(ctx, nil); err != nil || got != (SignIns{}) {
		t.Fatalf("delete with no origins and nothing without one: %+v, %v", got, err)
	}
}

// TestRecordingPeriod keeps one recording period across a clean restart
// and starts a new one after a longer gap or an unclean stop.
func TestRecordingPeriod(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	pairs := map[string]string{testRelay: testSignin}
	since := func() time.Time {
		t.Helper()
		rep, err := s.Retiring(ctx, testNow)
		if err != nil {
			t.Fatal(err)
		}
		return rep.RecordingSince
	}
	if rep, err := s.Retiring(ctx, testNow); err != nil || !rep.RecordingSince.IsZero() || !rep.RecordingStarted.IsZero() || !rep.RecordingStopped.IsZero() {
		t.Fatalf("recording period before any recording = %+v, %v", rep, err)
	}
	start := testNow.Add(-48 * time.Hour)
	for _, step := range []struct {
		stop, start time.Time
		want        time.Time
	}{
		{time.Time{}, start, start},
		{start.Add(time.Hour), start.Add(time.Hour + 5*time.Minute), start},
		{start.Add(2 * time.Hour), start.Add(3 * time.Hour), start.Add(3 * time.Hour)},
		{time.Time{}, start.Add(4 * time.Hour), start.Add(4 * time.Hour)},
	} {
		if !step.stop.IsZero() {
			if err := s.StopRecording(ctx, step.stop); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RecordPairs(ctx, pairs, step.start); err != nil {
			t.Fatal(err)
		}
		if got := since(); !got.Equal(step.want) {
			t.Errorf("stopped %v, started %v: recording since %v, want %v", step.stop, step.start, got, step.want)
		}
	}
	if err := s.StopRecording(ctx, start.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Retiring(ctx, testNow)
	if err != nil || !rep.RecordingSince.Equal(start.Add(4*time.Hour)) || !rep.RecordingStarted.Equal(start.Add(4*time.Hour)) ||
		!rep.RecordingStopped.Equal(start.Add(5*time.Hour)) {
		t.Fatalf("last known recording period = since %v, started %v, stopped %v, %v", rep.RecordingSince, rep.RecordingStarted, rep.RecordingStopped, err)
	}
}

// TestReportAcrossRepairing pairs a relay origin with another sign-in
// origin, and then a second relay origin with the same sign-in origin:
// tokens of a sign-in origin no pair names are reported apart, and a
// sign-in origin two pairs name is counted under the newer pair only.
func TestReportAcrossRepairing(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.SignIn(ctx, owner(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPairs(ctx, map[string]string{testRelay: testSignin}, testNow.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin)
		VALUES ('old', 'old-hash', ?, 'k', 'laptop', 0, ?, ?)`, id, unix(testNow), testSignin); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPairs(ctx, map[string]string{testRelay: testSignin2}, testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Retiring(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	one := Counts{Day: 1, Week: 1, Month: 1, Total: 1}
	if len(rep.Origins) != 1 || rep.Origins[0].SigninOrigin != testSignin2 || rep.Origins[0].DeviceTokens != (Counts{}) ||
		len(rep.Unpaired) != 1 || rep.Unpaired[0].SigninOrigin != testSignin || rep.Unpaired[0].DeviceTokens != one {
		t.Fatalf("after re-pairing %s with %s: %+v", testRelay, testSignin2, rep)
	}

	if err = s.RecordPairs(ctx, map[string]string{testRelay: testSignin2, testRelay2: testSignin}, testNow.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPairs(ctx, map[string]string{testRelay: testSignin}, testNow); err != nil {
		t.Fatal(err)
	}
	if rep, err = s.Retiring(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if len(rep.Origins) != 2 || len(rep.Unpaired) != 0 {
		t.Fatalf("two pairs naming %s: %+v", testSignin, rep)
	}
	newer, older := rep.Origins[0], rep.Origins[1]
	if newer.RelayOrigin != testRelay || newer.SigninCountedUnder != "" || newer.DeviceTokens != one ||
		older.RelayOrigin != testRelay2 || older.SigninCountedUnder != testRelay || older.DeviceTokens != (Counts{}) {
		t.Fatalf("two pairs naming %s: %+v", testSignin, rep.Origins)
	}
}

// TestReadRetiringDoesNotMigrate reads a schema 1 database through
// ReadRetiring: it reports that nothing is recorded and leaves the
// database and its backups as they were.
func TestReadRetiringDoesNotMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	closeStore(t, openSteps(t, path, migrations[:1]))
	_, err := ReadRetiring(context.Background(), path, testNow)
	if !errors.Is(err, ErrNoOrigins) || err.Error() != "edgestore: the database records no origins: "+path+" is at schema version 1" {
		t.Fatalf("ReadRetiring at schema 1 = %v", err)
	}
	if sch, err := ReadSchema(path); err != nil || sch.Version != 1 {
		t.Fatalf("schema after the report = %+v, %v", sch, err)
	}
	if _, err := os.Stat(filepath.Join(dir, BackupDir)); !os.IsNotExist(err) {
		t.Fatalf("the report made a backups directory: %v", err)
	}
	closeStore(t, openSteps(t, path, migrations))
	if rep, err := ReadRetiring(context.Background(), path, testNow); err != nil || rep.Origins == nil {
		t.Fatalf("ReadRetiring at schema 2 = %+v, %v", rep, err)
	}
}

// Retiring is ReadRetiring on an open store.
func (s *Store) Retiring(ctx context.Context, now time.Time) (Report, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only
	return retiring(ctx, tx, now)
}

// TestRecordingPeriodClockStep starts recording again after a clean stop
// that lies in the future, as after the clock stepped back: a new period
// begins, and a running edge never reads as stopped.
func TestRecordingPeriodClockStep(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	pairs := map[string]string{testRelay: testSignin}
	if err := s.RecordPairs(ctx, pairs, testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.StopRecording(ctx, testNow.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPairs(ctx, pairs, testNow); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Retiring(ctx, testNow)
	if err != nil || !rep.RecordingSince.Equal(testNow) || !rep.RecordingStarted.Equal(testNow) || !rep.RecordingStopped.IsZero() {
		t.Fatalf("after a stop in the future: since %v, started %v, stopped %v, %v", rep.RecordingSince, rep.RecordingStarted, rep.RecordingStopped, err)
	}
}
