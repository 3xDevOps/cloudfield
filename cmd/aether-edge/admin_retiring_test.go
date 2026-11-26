package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

var enrolledAt = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

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
		if err := s.RecordPairs(context.Background(), pairs, time.Now()); err != nil {
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
// pair, one without an origin) and one of mona's on A; a session of
// mona's on A; connector requests to both servers through A.
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
		if _, err := s.SignIn(ctx, a, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		id, name string
		owner    edgeproto.Account
	}{{testServer, "workstation", octo}, {testServer2, "lab", mona}} {
		if err := s.RecordClaim(ctx, c.id, c.name, edgeproto.PolicyAccount, edgeproto.AccountPrincipal(c.owner),
			edgestore.Enrollment{RelayOrigin: pairRelayA, AgentVersion: "v0.9.0", At: enrolledAt}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnrollServer(ctx, testServer, "workstation", edgeproto.PolicyAccount,
		edgestore.Enrollment{RelayOrigin: pairRelayB, AgentVersion: "v0.9.1", At: enrolledAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.DropOwner(ctx, testServer2); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id  string
		age time.Duration
	}{{testServer, 3 * 24 * time.Hour}, {testServer2, 2 * time.Hour}} {
		if err := s.UseConnector(ctx, u.id, pairRelayA, now.Add(-u.age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
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
		{`INSERT INTO devices SELECT 'octo-a', 'h1', id, 'k', 'laptop', 0, ?, ? FROM accounts WHERE login = 'octo'`, []any{now.UnixNano() - 10*day, pairSigninA}},
		{`INSERT INTO devices SELECT 'octo-b', 'h2', id, 'k', 'laptop', 0, ?, ? FROM accounts WHERE login = 'octo'`, []any{now.UnixNano(), pairSigninB}},
		{`INSERT INTO devices SELECT 'octo-bare', 'h3', id, 'k', 'old pc', 0, ?, NULL FROM accounts WHERE login = 'octo'`,
			[]any{time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC).UnixNano()}},
		{`INSERT INTO devices SELECT 'mona-a', 'h4', id, 'k', 'desk', 0, ?, ? FROM accounts WHERE login = 'mona'`, []any{now.UnixNano() - 40*day, pairSigninA}},
		{`INSERT INTO web_sessions SELECT 'mona-s', 'h5', id, 0, ?, ? FROM accounts WHERE login = 'mona'`,
			[]any{now.UnixNano() - 5*day + int64(edgeproto.SessionIdle), pairSigninA}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

func runRetiring(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := retiring(append([]string{"--data", dir}, args...), envOf(nil), &out)
	return out.String(), err
}

func TestRetiringReport(t *testing.T) {
	empty := retiringEdge(t, nil)
	if out, err := runRetiring(t, empty); err != nil || out != "no origin pairs recorded: the edge records the pairs it serves when it starts\n" {
		t.Fatalf("report of an empty store:\n%s%v", out, err)
	}
	out, err := runRetiring(t, empty, "--json")
	var empt struct {
		At      time.Time         `json:"at"`
		Origins []json.RawMessage `json:"origins"`
	}
	if err != nil || json.Unmarshal([]byte(out), &empt) != nil || empt.At.IsZero() || empt.Origins == nil || len(empt.Origins) != 0 {
		t.Fatalf("JSON report of an empty store:\n%s%v", out, err)
	}
	if _, err := runRetiring(t, empty, "--origin", pairRelayA); err == nil ||
		err.Error() != pairRelayA+" is not a relay origin this edge has served; it has served: -" {
		t.Fatalf("--origin on an empty store: %v", err)
	}

	one := retiringEdge(t, map[string]string{pairRelayA: pairSigninA})
	want := "relay origin " + pairRelayA + ", sign-in origin " + pairSigninA + `

no server last enrolled here

LAST USED WITHIN                                       1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                              0      0       0        0
accounts with a session issued here                    0      0       0        0
accounts with devices here and none at another origin  0      0       0        0
servers with connector requests through here           0      0       0        0
`
	if out, err := runRetiring(t, one); err != nil || out != want {
		t.Fatalf("report of one pair:\n%s%v\nwant:\n%s", out, err, want)
	}

	two := retiringEdge(t, map[string]string{pairRelayA: pairSigninA, pairRelayB: pairSigninB})
	populate(t, two)
	wantA := "relay origin " + pairRelayA + ", sign-in origin " + pairSigninA + `

SERVER ID                   NAME  OWNER LOGIN  BUILD   LAST ENROLLED
bbbbbbbbbbbbbbbbbbbbbbbbbb  lab   -            v0.9.0  2026-11-20T08:00:00Z

LAST USED WITHIN                                       1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                              0      0       1        2
accounts with a session issued here                    0      1       1        1
accounts with devices here and none at another origin  0      0       0        1
servers with connector requests through here           1      2       2        2
`
	wantB := "relay origin " + pairRelayB + ", sign-in origin " + pairSigninB + `

SERVER ID                   NAME         OWNER LOGIN  BUILD   LAST ENROLLED
aaaaaaaaaaaaaaaaaaaaaaaaaa  workstation  octo         v0.9.1  2026-11-20T08:00:00Z

LAST USED WITHIN                                       1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                              1      1       1        1
accounts with a session issued here                    0      0       0        0
accounts with devices here and none at another origin  0      0       0        0
servers with connector requests through here           0      0       0        0
`
	if out, err := runRetiring(t, two); err != nil || out != wantA+"\n"+wantB {
		t.Fatalf("report of two pairs:\n%s%v\nwant:\n%s\n%s", out, err, wantA, wantB)
	}
	if out, err := runRetiring(t, two, "--origin", strings.ToUpper(pairRelayB)); err != nil || out != wantB {
		t.Fatalf("report of --origin %s:\n%s%v", pairRelayB, out, err)
	}
	if _, err := runRetiring(t, two, "--origin", pairSigninA); err == nil ||
		err.Error() != pairSigninA+" is not a relay origin this edge has served; it has served: "+pairRelayA+", "+pairRelayB {
		t.Fatalf("--origin of a sign-in origin: %v", err)
	}
	if _, err := runRetiring(t, two, "--origin", "edge-a.example.test"); err == nil || !strings.HasPrefix(err.Error(), "--origin: ") {
		t.Fatalf("--origin without a scheme: %v", err)
	}

	out, err = runRetiring(t, two, "--json", "--origin", pairRelayA)
	var got struct {
		Origins []edgestore.OriginReport `json:"origins"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got.Origins) != 1 {
		t.Fatalf("JSON report of %s:\n%s%v", pairRelayA, out, err)
	}
	for _, key := range []string{`"relay_origin": "` + pairRelayA + `"`, `"signin_origin": "` + pairSigninA + `"`,
		`"servers": [`, `"id": "` + testServer2 + `"`, `"name": "lab"`, `"owner_login": ""`, `"build": "v0.9.0"`,
		`"last_enrolled_at": "2026-11-20T08:00:00Z"`,
		`"device_tokens": {
        "1d": 0,
        "7d": 0,
        "30d": 1,
        "total": 2
      }`, `"session_accounts": {`, `"device_only_accounts": {`, `"connector_servers": {
        "1d": 1,
        "7d": 2,`} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON report lacks %s:\n%s", key, out)
		}
	}
}

func runDevicesEnd(t *testing.T, dir, answer string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := devices(append([]string{"end", "--data", dir}, args...), envOf(nil), strings.NewReader(answer), &out)
	return out.String(), err
}

func deviceIDs(t *testing.T, dir string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	var ids string
	if err := db.QueryRow(`SELECT group_concat(id, ' ') FROM (SELECT id FROM devices ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestDevicesEnd(t *testing.T) {
	dir := retiringEdge(t, map[string]string{pairRelayA: pairSigninA, pairRelayB: pairSigninB})
	populate(t, dir)
	const all = "mona-a octo-a octo-b octo-bare"

	for _, bad := range []struct {
		args []string
		err  string
	}{
		{[]string{"--unused-since", "2026-11-01"}, "aether-edge devices end ends only device tokens with no recorded sign-in origin: pass --no-address"},
		{[]string{"--no-address"}, `--unused-since "" is not a YYYY-MM-DD date: parsing time "" as "2006-01-02": cannot parse "" as "2006"`},
		{[]string{"--no-address", "--unused-since", "2026-02-30"}, `--unused-since "2026-02-30" is not a YYYY-MM-DD date: parsing time "2026-02-30": day out of range`},
		{[]string{"--no-address", "--unused-since", "2026-11-01", "extra"}, "aether-edge devices end takes no arguments after its flags, got 1"},
	} {
		if _, err := runDevicesEnd(t, dir, "yes\n", bad.args...); err == nil || err.Error() != bad.err {
			t.Errorf("devices end %v: %v, want %s", bad.args, err, bad.err)
		}
	}
	if err := devices([]string{"list"}, envOf(nil), strings.NewReader(""), &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Errorf("devices list: %v, want the usage", err)
	}

	out, err := runDevicesEnd(t, dir, "yes\n", "--no-address", "--unused-since", "2026-10-01")
	if err != nil || out != "no device token without a sign-in origin was last used before 2026-10-01\n" || deviceIDs(t, dir) != all {
		t.Fatalf("devices end before the bare device's last use:\n%s%v", out, err)
	}

	list := `DEVICE ID  ACCOUNT    LOGIN OR EMAIL  LABEL   LAST USED
octo-bare  github:42  octo            old pc  2026-10-01T09:30:00Z
end the 1 device tokens listed, as signing out does? Type yes to end them: `
	for _, answer := range []string{"", "no\n", "y\n", "YES\n"} {
		out, err := runDevicesEnd(t, dir, answer, "--no-address", "--unused-since", "2026-10-02")
		if err != nil || out != list+"\nended nothing\n" || deviceIDs(t, dir) != all {
			t.Fatalf("devices end answered %q:\n%s%v; devices %s", answer, out, err, deviceIDs(t, dir))
		}
	}
	out, err = runDevicesEnd(t, dir, "yes\n", "--no-address", "--unused-since", "2026-10-02")
	if err != nil || out != list+"device tokens ended: 1; kept because they were used since the list was read: 0\n" {
		t.Fatalf("devices end answered yes:\n%s%v", out, err)
	}
	if got := deviceIDs(t, dir); got != "mona-a octo-a octo-b" {
		t.Fatalf("devices after ending: %s", got)
	}
}
