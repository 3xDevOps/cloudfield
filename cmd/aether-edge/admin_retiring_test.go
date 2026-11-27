package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

const (
	pairSigninA = "https://auth-a.example.test"
	pairRelayA  = "https://edge-a.example.test"
	pairSigninB = "https://auth-b.example.test"
	pairRelayB  = "https://edge-b.example.test"
	testServer2 = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	enrolledAt     = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	recordingSince = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

// retiringEdge is a data directory whose edge served pairs, with no one
// on them.
func retiringEdge(t *testing.T, pairs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if pairs != nil {
		if _, err := s.RecordPairs(context.Background(), edgestore.Run{Pairs: pairs}, recordingSince); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// populate puts on the two pairs: octo's server, which moved from A to B,
// and an ownerless one on A; three device tokens of octo's (one on each
// pair, one without an origin) and two of mona's on A, one of them
// first seen there; a session of mona's on A; connector requests to both
// servers through A.
func populate(t *testing.T, dir string) {
	t.Helper()
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	octo := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"}
	mona := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "43", Login: "mona"}
	for _, a := range []edgeproto.Account{octo, mona} {
		if _, err = s.SignIn(ctx, a, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		id, name string
		owner    edgeproto.Account
	}{{testServer, "workstation", octo}, {testServer2, "lab", mona}} {
		if err = s.RecordClaim(ctx, c.id, c.name, edgeproto.PolicyAccount, edgeproto.AccountPrincipal(c.owner),
			edgestore.Enrollment{RelayOrigin: pairRelayA, AgentVersion: "v0.9.0", At: enrolledAt}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.EnrollServer(ctx, testServer, "workstation", edgeproto.PolicyAccount,
		edgestore.Enrollment{RelayOrigin: pairRelayB, AgentVersion: "v0.9.1", At: enrolledAt}); err != nil {
		t.Fatal(err)
	}
	if err = s.DropOwner(ctx, testServer2); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id  string
		age time.Duration
	}{{testServer, 3 * 24 * time.Hour}, {testServer2, 2 * time.Hour}} {
		if err = s.UseConnector(ctx, u.id, pairRelayA, now.Add(-u.age)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	day := int64(24 * time.Hour)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{insertDevice + `'octo-a', 'h1', id, 'k', 'laptop', 0, ?, ?, NULL FROM accounts WHERE login = 'octo'`, []any{now.UnixNano() - 10*day, pairSigninA}},
		{insertDevice + `'octo-b', 'h2', id, 'k', 'laptop', 0, ?, ?, NULL FROM accounts WHERE login = 'octo'`, []any{now.UnixNano(), pairSigninB}},
		{insertDevice + `'octo-bare', 'h3', id, 'k', 'old pc', 0, ?, NULL, NULL FROM accounts WHERE login = 'octo'`, []any{now.UnixNano() - 2*day}},
		{insertDevice + `'mona-a', 'h4', id, 'k', 'desk', 0, ?, ?, NULL FROM accounts WHERE login = 'mona'`, []any{now.UnixNano() - 40*day, pairSigninA}},
		{insertDevice + `'mona-seen', 'h6', id, 'k', 'desk', 0, ?, ?, 1 FROM accounts WHERE login = 'mona'`, []any{now.UnixNano() - 6*day, pairSigninA}},
		{`INSERT INTO web_sessions (id, token_hash, account_id, created_at, expires_at, signin_origin) SELECT 'mona-s', 'h5', id, 0, ?, ? FROM accounts WHERE login = 'mona'`,
			[]any{now.UnixNano() - 5*day + int64(edgeproto.SessionIdle), pairSigninA}},
	} {
		if _, err = db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

const insertDevice = `INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin, signin_origin_seen) SELECT `

func runRetiring(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := retiring(append([]string{"--data", dir}, args...), envOf(nil), &out)
	return out.String(), err
}

func TestRetiringReport(t *testing.T) {
	empty := retiringEdge(t, nil)
	wantEmpty := `no origin pairs recorded: the edge records them from its first start with an option that retires a pair

unknown origin: 0 claimed servers not enrolled or claimed since recording began

LAST USED WITHIN         1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens            0      0       0        0
accounts with a session  0      0       0        0

` + retiringCaveat
	if out, err := runRetiring(t, empty); err != nil || out != wantEmpty {
		t.Fatalf("report of an empty store:\n%s%v\nwant:\n%s", out, err, wantEmpty)
	}
	out, err := runRetiring(t, empty, "--json")
	var empt struct {
		At             time.Time         `json:"at"`
		RecordingSince time.Time         `json:"recording_since"`
		Origins        []json.RawMessage `json:"origins"`
		Unknown        edgestore.Unknown `json:"unknown"`
	}
	if err != nil || json.Unmarshal([]byte(out), &empt) != nil || empt.At.IsZero() || !empt.RecordingSince.IsZero() ||
		empt.Origins == nil || len(empt.Origins) != 0 || !strings.Contains(out, `"origins": [],`) {
		t.Fatalf("JSON report of an empty store:\n%s%v", out, err)
	}
	if _, err = runRetiring(t, empty, "--origin", pairRelayA); err == nil ||
		err.Error() != pairRelayA+" is not a relay origin this edge has recorded; it has recorded: -" {
		t.Fatalf("--origin on an empty store: %v", err)
	}

	one := retiringEdge(t, map[string]string{pairRelayA: pairSigninA})
	want := `last known recording period: since 2026-11-01T00:00:00Z, last started 2026-11-01T00:00:00Z; the edge is recording now, or stopped uncleanly

relay origin ` + pairRelayA + `, sign-in origin ` + pairSigninA + `

no server last enrolled or claimed here

LAST USED WITHIN                                                      1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                                             0      0       0        0
device tokens issued earlier, first seen here                         0      0       0        0
accounts with a session here                                          0      0       0        0
accounts with device tokens here and none recorded at another origin  0      0       0        0
servers with connector requests through here                          0      0       0        0

unknown origin: 0 claimed servers not enrolled or claimed since recording began

LAST USED WITHIN         1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens            0      0       0        0
accounts with a session  0      0       0        0

` + retiringCaveat
	if out, err = runRetiring(t, one); err != nil || out != want {
		t.Fatalf("report of one pair:\n%s%v\nwant:\n%s", out, err, want)
	}

	two := retiringEdge(t, map[string]string{pairRelayA: pairSigninA, pairRelayB: pairSigninB})
	populate(t, two)
	wantA := `
relay origin ` + pairRelayA + `, sign-in origin ` + pairSigninA + `

SERVER ID                   NAME  OWNER LOGIN  BUILD   LAST ENROLLED OR CLAIMED
bbbbbbbbbbbbbbbbbbbbbbbbbb  lab   -            v0.9.0  2026-11-20T08:00:00Z

LAST USED WITHIN                                                      1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                                             0      0       1        2
device tokens issued earlier, first seen here                         0      1       1        1
accounts with a session here                                          0      1       1        1
accounts with device tokens here and none recorded at another origin  0      1       1        1
servers with connector requests through here                          1      2       2        2
`
	wantB := `
relay origin ` + pairRelayB + `, sign-in origin ` + pairSigninB + `

SERVER ID                   NAME         OWNER LOGIN  BUILD   LAST ENROLLED OR CLAIMED
aaaaaaaaaaaaaaaaaaaaaaaaaa  workstation  octo         v0.9.1  2026-11-20T08:00:00Z

LAST USED WITHIN                                                      1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                                             1      1       1        1
device tokens issued earlier, first seen here                         0      0       0        0
accounts with a session here                                          0      0       0        0
accounts with device tokens here and none recorded at another origin  0      0       0        0
servers with connector requests through here                          0      0       0        0
`
	unknown := `
unknown origin: 0 claimed servers not enrolled or claimed since recording began

LAST USED WITHIN         1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens            0      1       1        1
accounts with a session  0      0       0        0

` + retiringCaveat
	if out, err = runRetiring(t, two); err != nil || out != "last known recording period: since 2026-11-01T00:00:00Z, last started 2026-11-01T00:00:00Z; the edge is recording now, or stopped uncleanly\n"+wantA+wantB+unknown {
		t.Fatalf("report of two pairs:\n%s%v\nwant:\n%s%s%s", out, err, wantA, wantB, unknown)
	}
	if out, err = runRetiring(t, two, "--origin", strings.ToUpper(pairRelayB)); err != nil || out != "last known recording period: since 2026-11-01T00:00:00Z, last started 2026-11-01T00:00:00Z; the edge is recording now, or stopped uncleanly\n"+wantB+unknown {
		t.Fatalf("report of --origin %s:\n%s%v", pairRelayB, out, err)
	}
	if _, err = runRetiring(t, two, "--origin", pairSigninA); err == nil ||
		err.Error() != pairSigninA+" is not a relay origin this edge has recorded; it has recorded: "+pairRelayA+", "+pairRelayB {
		t.Fatalf("--origin of a sign-in origin: %v", err)
	}
	if _, err = runRetiring(t, two, "--origin", "edge-a.example.test"); err == nil || !strings.HasPrefix(err.Error(), "--origin: ") {
		t.Fatalf("--origin without a scheme: %v", err)
	}

	out, err = runRetiring(t, two, "--json", "--origin", pairRelayA)
	var got struct {
		Origins []edgestore.OriginReport `json:"origins"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got.Origins) != 1 {
		t.Fatalf("JSON report of %s:\n%s%v", pairRelayA, out, err)
	}
	for _, key := range []string{`"recording_since": "2026-11-01T00:00:00Z"`, `"relay_origin": "` + pairRelayA + `"`, `"signin_origin": "` + pairSigninA + `"`,
		`"servers": [`, `"id": "` + testServer2 + `"`, `"name": "lab"`, `"owner_login": ""`, `"build": "v0.9.0"`,
		`"last_seen_at": "2026-11-20T08:00:00Z"`,
		`"device_tokens": {
        "1d": 0,
        "7d": 0,
        "30d": 1,
        "total": 2
      }`, `"device_tokens_seen": {`, `"session_accounts": {`, `"device_only_accounts": {`, `"connector_servers": {
        "1d": 1,
        "7d": 2,`, `"unknown": {
    "device_tokens": {
      "1d": 0,
      "7d": 1,
      "30d": 1,
      "total": 1
    },`, `"servers": 0`} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON report lacks %s:\n%s", key, out)
		}
	}
}

// TestRetiringOnSchema1 reports on a stopped edge's schema 1 database
// without migrating it or backing it up.
func TestRetiringOnSchema1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL, compat_floor INTEGER);
		INSERT INTO schema_migrations VALUES (1, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := runRetiring(t, dir)
	if want := "nothing is recorded yet: edgestore: the database records no origins: " + path +
		" is at schema version 1, and an edge records origins from schema 2 on\n"; err != nil || out != want {
		t.Fatalf("report on schema 1:\n%s%v\nwant:\n%s", out, err, want)
	}
	out, err = runRetiring(t, dir, "--json")
	var status struct {
		At            time.Time `json:"at"`
		Status        string    `json:"status"`
		Schema        int       `json:"schema"`
		OriginsSchema int       `json:"origins_schema"`
	}
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || status.At.IsZero() ||
		status.Status != "not_recorded" || status.Schema != 1 || status.OriginsSchema != 2 {
		t.Fatalf("JSON report on schema 1:\n%s%v", out, err)
	}
	if s, err := edgestore.ReadSchema(path); err != nil || s.Version != 1 {
		t.Fatalf("schema after the report = %+v, %v", s, err)
	}
	if _, err := os.Stat(filepath.Join(dir, edgestore.BackupDir)); !os.IsNotExist(err) {
		t.Fatalf("the report made a backups directory: %v", err)
	}
}

// TestRetiringQuotesNames prints a server name holding a bidi control
// quoted, so it cannot reorder the line.
func TestRetiringQuotesNames(t *testing.T) {
	dir := retiringEdge(t, map[string]string{pairRelayA: pairSigninA})
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	octo := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"}
	if _, err = s.SignIn(ctx, octo, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordClaim(ctx, testServer, "lab\u202eexe.txt\x7f", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(octo),
		edgestore.Enrollment{RelayOrigin: pairRelayA, AgentVersion: "v1\u200b", At: enrolledAt}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.StopRecording(ctx, recordingSince.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := runRetiring(t, dir)
	if err != nil || !strings.Contains(out, testServer+`  "lab\u202eexe.txt\x7f"  octo         "v1\u200b"  2026-11-20T08:00:00Z`) ||
		!strings.HasPrefix(out, "last known recording period: since 2026-11-01T00:00:00Z, last started 2026-11-01T00:00:00Z; stopped cleanly 2026-11-02T00:00:00Z\n") {
		t.Fatalf("report with a bidi control in a name:\n%s%v", out, err)
	}
	out, err = runRetiring(t, dir, "--json")
	var got struct {
		Status  string                   `json:"status"`
		Origins []edgestore.OriginReport `json:"origins"`
	}
	if err != nil || strings.ContainsAny(out, "\u202e\u200b\x7f") || !strings.Contains(out, `"name": "lab\u202eexe.txt\u007f"`) ||
		!strings.Contains(out, `"build": "v1\u200b"`) || json.Unmarshal([]byte(out), &got) != nil || got.Status != "recorded" ||
		len(got.Origins) != 1 || len(got.Origins[0].Servers) != 1 ||
		got.Origins[0].Servers[0].Name != "lab\u202eexe.txt\x7f" || got.Origins[0].Servers[0].AgentVersion != "v1\u200b" {
		t.Fatalf("JSON report with a bidi control in a name:\n%s%v", out, err)
	}
}

// TestRetiringCleanStopAfterClockStep reports a run that stopped cleanly
// at a time before its start, as after the clock stepped back, as stopped.
func TestRetiringCleanStopAfterClockStep(t *testing.T) {
	dir := retiringEdge(t, map[string]string{pairRelayA: pairSigninA})
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StopRecording(context.Background(), recordingSince.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := runRetiring(t, dir)
	if want := "last known recording period: since 2026-11-01T00:00:00Z, last started 2026-11-01T00:00:00Z; stopped cleanly 2026-10-31T23:00:00Z\n"; err != nil || !strings.HasPrefix(out, want) {
		t.Fatalf("report after a clean stop before the start:\n%s%v\nwant it to start with:\n%s", out, err, want)
	}
}
