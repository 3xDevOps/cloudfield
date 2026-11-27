package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeagent "github.com/3xDevOps/cloudfield/internal/edge/agent"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/serversetup"
)

// edge status prints a retiring row from the notices the server stored
// for the address edge-url names, unless the running server is at another
// address, and cleans the error text of a refused dial.
func TestEdgeStatusRetiringRow(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	const old, other = "https://edge-old.example.test", "https://edge-new.example.test"
	now := time.Now()
	since := now.Add(-time.Hour)
	connected := edgeagent.Status{Edge: old, Connected: true, Since: since, PID: os.Getpid(), Heartbeat: now}
	gone := "dial " + old + "/v1/server/control: 410 Gone: " + old + " is retired; this edge is at " + other
	retiring := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: old, Replacement: other}
	dated := retiring
	dated.Date = "2027-03-01"
	elsewhere := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: "https://edge-third.example.test", Replacement: other}
	tests := []struct {
		name    string
		status  edgeagent.Status
		notices edgeagent.StoredNotices
		config  string
		// raw, when set, is written as the notices file instead.
		raw        string
		row        string
		connection string
		err        error
	}{
		{name: "no notice", status: connected},
		{name: "a notice for another address", status: connected, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{elsewhere}}},
		{name: "retiring", status: connected, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{elsewhere, retiring}},
			row: "retiring     " + old + " is retiring; move this server with: sudo cloudfield-server edge move " + other},
		{name: "retiring on a date", status: connected, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row: "retiring     " + old + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move " + other},
		{name: "another config file", status: connected, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}}, config: "/etc/aether/other.conf",
			row: "retiring     " + old + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move " + other + " --config /etc/aether/other.conf"},
		{name: "first use", status: connected, notices: edgeagent.StoredNotices{FirstUse: true, Notices: []edgeproto.Notice{retiring}},
			row: "retiring     " + old + " is retiring; move this server with: sudo cloudfield-server edge move " + other +
				" (the edge sent this with the connection that pinned its key, so it is trusted on first use and shown only)"},
		{name: "the running server is elsewhere", status: edgeagent.Status{Edge: other, Connected: true, Since: since, PID: os.Getpid(), Heartbeat: now},
			notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row:     "running      the running server is at " + other + "; restart to apply edge-url: sudo systemctl restart aether-server"},
		{name: "moved off a retired address, not restarted", status: edgeagent.Status{Edge: other, Error: gone, Since: since},
			notices:    edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row:        "running      the server last dialed " + other + "; restart to apply edge-url: sudo systemctl restart aether-server",
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone, err: errNotConnected},
		{name: "last dialed another address, no notice", status: edgeagent.Status{Edge: other, Error: gone, Since: since},
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone, err: errNotConnected},
		{name: "running at the address with its port written out", status: edgeagent.Status{Edge: old + ":443", Connected: true, Since: since, PID: os.Getpid(), Heartbeat: now},
			notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{retiring}},
			row:     "retiring     " + old + " is retiring; move this server with: sudo cloudfield-server edge move " + other},
		{name: "a damaged entry that still parses", status: connected,
			notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: old, Replacement: "not-a-url"}}}},
		{name: "a stale connection at another address", status: edgeagent.Status{Edge: other, Connected: true, Since: since, PID: os.Getpid(), Heartbeat: now.Add(-time.Hour)},
			notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row:     "running      the server last dialed " + other + "; restart to apply edge-url: sudo systemctl restart aether-server", err: errNotConnected},
		{name: "an unreadable notices file", status: connected, raw: "{not json",
			row:        "notices      unreadable, so none is shown: edgeagent: ",
			connection: "connection   connected since " + since.Local().Format(time.DateTime) + "; the edge last answered 0s ago"},
		{name: "retired since", status: edgeagent.Status{Edge: old, Error: gone, Since: since}, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row:        "retiring     " + old + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move " + other,
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone, err: errNotConnected},
		{name: "control characters", status: edgeagent.Status{Edge: old, Error: gone + "\r\x1b[2K\nconnected\u202e\u2028", Since: since},
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone + "??[2K?connected??", err: errNotConnected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			id, err := serverID(dir)
			if err != nil {
				t.Fatal(err)
			}
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			state := edgeagent.OpenState(dir)
			if err := state.Pin(pub); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(edgeagent.StateDir(dir), "status.json"), tt.status)
			if tt.notices.Notices != nil {
				tt.notices.Received, tt.notices.ServerID = since, id
				writeJSON(t, noticesPath(dir, pub), tt.notices)
			}
			if tt.raw != "" {
				if err := os.MkdirAll(filepath.Dir(noticesPath(dir, pub)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(noticesPath(dir, pub), []byte(tt.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.notices.Notices != nil || tt.raw != "" {
				// The agent binds its notices to the pin file it verified them under.
				pinLink := filepath.Join(filepath.Dir(noticesPath(dir, pub)), "notices.pin")
				if err := os.Link(filepath.Join(edgeagent.StateDir(dir), "edge_key.json"), pinLink); err != nil {
					t.Fatal(err)
				}
			}
			config := tt.config
			if config == "" {
				config = serversetup.DefaultConfigPath
			}
			var out bytes.Buffer
			if err := edgeStatus(&out, dir, old, config, edgeproto.PolicyAccount, now); err != tt.err {
				t.Fatalf("status = %v, want %v", err, tt.err)
			}
			var rows, connections []string
			for line := range strings.SplitSeq(out.String(), "\n") {
				switch {
				case strings.HasPrefix(line, "retiring"), strings.HasPrefix(line, "running"), strings.HasPrefix(line, "notices"):
					rows = append(rows, line)
				case strings.HasPrefix(line, "connection"):
					connections = append(connections, line)
				}
			}
			want := []string{}
			if tt.row != "" {
				want = []string{tt.row}
			}
			matches := func(got, want string) bool { return got == want || tt.raw != "" && strings.HasPrefix(got, want) }
			if len(rows) != len(want) || len(want) == 1 && !matches(rows[0], want[0]) {
				t.Errorf("rows:\n%q\nwant\n%q\nin:\n%s", rows, want, out.String())
			}
			if len(connections) != 1 || tt.connection != "" && connections[0] != tt.connection {
				t.Errorf("connection rows:\n%q\nwant one: %q", connections, tt.connection)
			}
		})
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noticesPath is where the agent stores the notices for the pinned key
// pub.
func noticesPath(dataDir string, pub ed25519.PublicKey) string {
	id := strings.NewReplacer("+", "-", "/", "_").Replace(strings.TrimPrefix(edgeproto.EdgeKeyFingerprint(pub), "SHA256:"))
	return filepath.Join(edgeagent.StateDir(dataDir), "keys", "sha256-"+id, "notices.json")
}

// A move off a retired or retiring address writes edge-url and says how
// to apply it, with no way back; off a retired one with no claim that
// what was made there keeps working. Under another pin it writes nothing.
func TestApplyMoveFromARetiringOrRetiredAddress(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	const from, to = "https://edge-old.example.test", "https://edge-new.example.test"
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	enrolled := "enrolled at " + to + ", an address of the same edge (edge key " + edgeproto.EdgeKeyFingerprint(pub) + ", still pinned); "
	for _, tc := range []struct {
		name  string
		moved edgeagent.Moved
		// meanwhile changes the state or config while the move runs.
		meanwhile func(dir, path string) error
		want      string
		err       string
	}{
		{name: "retired", moved: edgeagent.Moved{To: to, Retired: true},
			want: enrolled + "the edge says " + from + " is retired\n"},
		{name: "retiring", moved: edgeagent.Moved{To: to, Retiring: true},
			want: enrolled + "the edge announces " + from + " too, so links and connectors made there keep working until it is retired; " +
				"it is retiring, so there is no moving back\n"},
		{name: "another pin since", moved: edgeagent.Moved{To: to, Retired: true},
			meanwhile: func(dir, _ string) error { return edgeagent.OpenState(dir).Pin(other) },
			err:       "the pinned edge key changed from " + edgeproto.EdgeKeyFingerprint(pub) + " while this ran; nothing changed, run it again"},
		{name: "the same key pinned again since", moved: edgeagent.Moved{To: to},
			meanwhile: func(dir, _ string) error { return edgeagent.OpenState(dir).Pin(pub) },
			err:       "the pinned edge key changed from " + edgeproto.EdgeKeyFingerprint(pub) + " while this ran; nothing changed, run it again"},
		{name: "edge-url given by a flag", moved: edgeagent.Moved{To: to},
			meanwhile: func(dir, path string) error {
				return serversetup.WriteConfig(path, map[string]string{"data-dir": dir})
			}},
		{name: "the edge turned off since", moved: edgeagent.Moved{To: to},
			meanwhile: func(dir, path string) error {
				return serversetup.WriteConfig(path, map[string]string{"data-dir": dir, "edge-url": ""})
			}},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "server.conf")
		if err := serversetup.WriteConfig(path, map[string]string{"data-dir": dir, "edge-url": from}); err != nil {
			t.Fatal(err)
		}
		state := edgeagent.OpenState(dir)
		if err := state.Pin(pub); err != nil {
			t.Fatal(err)
		}
		held, err := state.HoldPin()
		if err != nil {
			t.Fatal(err)
		}
		wantURL := from
		if tc.meanwhile != nil {
			if err = tc.meanwhile(dir, path); err != nil {
				t.Fatal(err)
			}
			if tc.err == "" {
				tc.err = path + ` gives edge-url "" and data-dir "` + dir + `", but edge move ran with "` + from + `" and "` + dir +
					`": the file changed since, or a flag overrode it; nothing changed, run it again without --edge-url or --data-dir`
				wantURL = ""
			}
		}
		var out bytes.Buffer
		err = applyMove(&out, dir, from, path, held, tc.moved)
		held.Close()
		want := tc.want
		if tc.err == "" {
			want += path + ": edge-url = " + to + " (was " + from + ")\napply it with:\n  sudo systemctl restart aether-server\n"
			wantURL = to
		}
		if tc.err == "" && err != nil || tc.err != "" && fmt.Sprint(err) != tc.err {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.err)
		}
		if out.String() != want {
			t.Errorf("%s: move output:\n%s\nwant:\n%s", tc.name, out.String(), want)
		}
		if got, err := serversetup.Load(path); err != nil || got["edge-url"] != wantURL {
			t.Errorf("%s: config after the move: %v, %v", tc.name, got, err)
		}
	}
}
