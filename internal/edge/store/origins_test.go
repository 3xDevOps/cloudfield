package edgestore

import (
	"context"
	"database/sql"
	"errors"
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
	if _, err := s.db.Exec(`INSERT INTO devices VALUES ('dev', 'token-hash', 1, 'ssh-ed25519 AAAA', 'laptop', 0, 0, ?)`, testSignin); err != nil {
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

	addDevice(t, s, id, "issued", "issued-hash")
	if _, err := s.db.Exec(`UPDATE devices SET signin_origin = ? WHERE id = 'issued'`, testSignin2); err != nil {
		t.Fatal(err)
	}
	auth := DeviceAuth{CodeHash: "code-new", UserCodeHash: "user-new", Label: "phone", Key: "ssh-ed25519 BBBB",
		CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour)}
	if err := s.CreateDeviceAuth(ctx, auth); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceAuth(ctx, auth.UserCodeHash, id, true, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemDeviceAuth(ctx, auth.CodeHash, Device{ID: "new", TokenHash: "new-hash", SigninOrigin: testSignin}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO devices VALUES ('bare', 'bare-hash', ?, 'ssh-ed25519 CCCC', 'old', 0, 0, NULL)`, id); err != nil {
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
	for _, q := range []string{
		`UPDATE devices SET last_used_at = ?, signin_origin = COALESCE(signin_origin, NULLIF(?, '')) WHERE token_hash = ? AND ` + notBlocked("devices.account_id"),
		`UPDATE web_sessions SET expires_at = ?, signin_origin = COALESCE(signin_origin, NULLIF(?, '')) WHERE token_hash = ? AND expires_at > ? AND ` + notBlocked("web_sessions.account_id"),
		`UPDATE servers SET name = ?, access_policy = ?, relay_origin = ?, agent_version = ?, enrolled_at = ? WHERE id = ?`,
		`INSERT INTO connector_use (server_id, relay_origin, last_used_at) SELECT id, ?, ? FROM servers WHERE id = ?
			ON CONFLICT (server_id, relay_origin) DO UPDATE SET last_used_at = excluded.last_used_at`,
	} {
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

// TestOriginReports reports an empty store, then two pairs: a server that
// moved shows at its new relay origin only, and each count falls in the
// windows its last use is within.
func TestOriginReports(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if got, err := s.OriginReports(ctx, testNow); err != nil || got != nil {
		t.Fatalf("reports of an empty store = %+v, %v", got, err)
	}
	if err := s.RecordPairs(ctx, map[string]string{testRelay: testSignin}, testNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.OriginReports(ctx, testNow)
	if err != nil || len(got) != 1 || got[0].RelayOrigin != testRelay || got[0].SigninOrigin != testSignin ||
		len(got[0].Servers) != 0 || got[0].DeviceTokens != (Counts{}) || got[0].ConnectorServers != (Counts{}) {
		t.Fatalf("report of one pair with nobody on it = %+v, %v", got, err)
	}
	if err := s.RecordPairs(ctx, map[string]string{testRelay: testSignin, testRelay2: testSignin2}, testNow); err != nil {
		t.Fatal(err)
	}

	me, other := owner(), edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	claim(t, s, serverA, "alpha", me)
	claim(t, s, serverB, "beta", other)
	if _, err := s.EnrollServer(ctx, serverA, "alpha", edgeproto.PolicyAccount,
		Enrollment{RelayOrigin: testRelay2, AgentVersion: "v2.0.0", At: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.DropOwner(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	myID, _ := s.SignIn(ctx, me, testNow)
	otherID, _ := s.SignIn(ctx, other, testNow)
	day := 24 * time.Hour
	for _, d := range []struct {
		id      string
		account int64
		origin  any
		age     time.Duration
	}{
		{"d1", myID, testSignin, time.Hour},
		{"d2", myID, testSignin, 2 * day},
		{"d3", myID, testSignin2, 0},
		{"d4", otherID, testSignin, 10 * day},
		{"d5", otherID, testSignin, 40 * day},
		{"d6", otherID, nil, 0},
	} {
		if _, err := s.db.Exec(`INSERT INTO devices VALUES (?, ?, ?, 'ssh-ed25519 AAAA', 'laptop', 0, ?, ?)`,
			d.id, d.id+"-hash", d.account, unix(testNow.Add(-d.age)), d.origin); err != nil {
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
		{"s3", otherID, testSignin, 31 * day},
		{"s4", otherID, testSignin2, 0},
	} {
		used := testNow.Add(-sess.age)
		if err := s.CreateSession(ctx, sess.id, sess.id+"-hash", sess.account, sess.origin, used, used.Add(edgeproto.SessionIdle)); err != nil {
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
		if err := s.UseConnector(ctx, u.id, u.origin, testNow.Add(-u.age)); err != nil {
			t.Fatal(err)
		}
	}

	got, err = s.OriginReports(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []OriginReport{{
		RelayOrigin: testRelay, SigninOrigin: testSignin,
		Servers:            []EnrolledServer{{ID: serverB, Name: "beta", AgentVersion: "v1.0.0", EnrolledAt: testNow}},
		DeviceTokens:       Counts{Day: 1, Week: 2, Month: 3, Total: 4},
		SessionAccounts:    Counts{Day: 0, Week: 1, Month: 1, Total: 1},
		DeviceOnlyAccounts: Counts{Day: 0, Week: 0, Month: 1, Total: 1},
		ConnectorServers:   Counts{Day: 0, Week: 1, Month: 2, Total: 2},
	}, {
		RelayOrigin: testRelay2, SigninOrigin: testSignin2,
		Servers:            []EnrolledServer{{ID: serverA, Name: "alpha", OwnerLogin: "Owner", AgentVersion: "v2.0.0", EnrolledAt: testNow}},
		DeviceTokens:       Counts{Day: 1, Week: 1, Month: 1, Total: 1},
		SessionAccounts:    Counts{Day: 1, Week: 1, Month: 1, Total: 1},
		DeviceOnlyAccounts: Counts{},
		ConnectorServers:   Counts{Day: 1, Week: 1, Month: 1, Total: 1},
	}}
	if len(got) != len(want) {
		t.Fatalf("reports = %+v, want %+v", got, want)
	}
	for i := range want {
		if g, w := got[i], want[i]; !slices.Equal(g.Servers, w.Servers) || g.RelayOrigin != w.RelayOrigin || g.SigninOrigin != w.SigninOrigin ||
			g.DeviceTokens != w.DeviceTokens || g.SessionAccounts != w.SessionAccounts ||
			g.DeviceOnlyAccounts != w.DeviceOnlyAccounts || g.ConnectorServers != w.ConnectorServers {
			t.Errorf("report = %+v\nwant     %+v", g, w)
		}
	}
}

// TestEndUnaddressedDevices lists and ends only the device tokens with no
// sign-in origin last used before the date, and keeps one used since the
// list was read.
func TestEndUnaddressedDevices(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.SignIn(ctx, owner(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, d := range []struct {
		id     string
		origin any
		used   time.Time
	}{
		{"old-bare", nil, before.Add(-time.Hour)},
		{"older-bare", nil, before.Add(-48 * time.Hour)},
		{"raced", nil, before.Add(-2 * time.Hour)},
		{"old-addressed", testSignin, before.Add(-time.Hour)},
		{"new-bare", nil, before},
	} {
		if _, err := s.db.Exec(`INSERT INTO devices VALUES (?, ?, ?, 'ssh-ed25519 AAAA', 'laptop', 0, ?, ?)`,
			d.id, d.id+"-hash", id, unix(d.used), d.origin); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.UnaddressedDevices(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range list {
		ids = append(ids, d.ID)
	}
	if want := []string{"older-bare", "raced", "old-bare"}; !slices.Equal(ids, want) || list[0].Account.Login != "Owner" {
		t.Fatalf("unaddressed devices = %+v, want %v", list, want)
	}
	if _, err := s.UseDevice(ctx, "raced-hash", testSignin2, testNow); err != nil {
		t.Fatal(err)
	}
	ended, err := s.EndUnaddressedDevices(ctx, ids, before)
	if err != nil || !slices.Equal(ended, []string{"older-bare", "old-bare"}) {
		t.Fatalf("ended = %v, %v", ended, err)
	}
	if n := countRows(t, s, "devices"); n != 3 {
		t.Fatalf("%d devices left, want raced, old-addressed and new-bare", n)
	}
}
