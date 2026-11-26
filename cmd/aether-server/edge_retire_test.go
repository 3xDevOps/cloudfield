package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	t.Setenv("SUDO_USER", "")
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
		name       string
		status     edgeagent.Status
		notices    edgeagent.StoredNotices
		config     string
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
			row:     "running      the running server is at " + other + "; restart to apply edge-url: systemctl restart aether-server"},
		{name: "retired since", status: edgeagent.Status{Edge: old, Error: gone, Since: since}, notices: edgeagent.StoredNotices{Notices: []edgeproto.Notice{dated}},
			row:        "retiring     " + old + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move " + other,
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone, err: errNotConnected},
		{name: "control characters", status: edgeagent.Status{Edge: old, Error: gone + "\r\x1b[2K\nconnected\u202e", Since: since},
			connection: "connection   disconnected since " + since.Local().Format(time.DateTime) + ": " + gone + "[2Kconnected", err: errNotConnected},
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
			config := tt.config
			if config == "" {
				config = serversetup.DefaultConfigPath
			}
			var out bytes.Buffer
			if err := edgeStatus(&out, dir, old, config, edgeproto.PolicyAccount, now); err != tt.err {
				t.Fatalf("status = %v, want %v", err, tt.err)
			}
			var rows []string
			for line := range strings.SplitSeq(out.String(), "\n") {
				if strings.HasPrefix(line, "retiring") || strings.HasPrefix(line, "running") {
					rows = append(rows, line)
				}
				if tt.connection != "" && strings.HasPrefix(line, "connection") && line != tt.connection {
					t.Errorf("connection row:\n%q\nwant\n%q", line, tt.connection)
				}
			}
			if want := []string{tt.row}; tt.row == "" && len(rows) != 0 || tt.row != "" && (len(rows) != 1 || rows[0] != want[0]) {
				t.Errorf("rows:\n%q\nwant\n%q\nin:\n%s", rows, tt.row, out.String())
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

// A move off a retired address writes edge-url and says how to apply it,
// with no way back and no claim that what was made there keeps working.
func TestApplyMoveFromARetiredAddress(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	dir := t.TempDir()
	path := filepath.Join(dir, "server.conf")
	const from, to = "https://edge-old.example.test", "https://edge-new.example.test"
	if err := serversetup.WriteConfig(path, map[string]string{"data-dir": dir, "edge-url": from}); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := edgeagent.OpenState(dir).Pin(pub); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := applyMove(&out, dir, from, to, path, true); err != nil {
		t.Fatal(err)
	}
	want := "enrolled at " + to + ", an address of the same edge (edge key " + edgeproto.EdgeKeyFingerprint(pub) + ", still pinned); " +
		"the edge says " + from + " is retired\n" +
		path + ": edge-url = " + to + " (was " + from + ")\n" +
		"apply it with:\n  sudo systemctl restart aether-server\n"
	if out.String() != want {
		t.Errorf("move output:\n%s\nwant:\n%s", out.String(), want)
	}
	if got, err := serversetup.Load(path); err != nil || got["edge-url"] != to {
		t.Errorf("config after the move: %v, %v", got, err)
	}
}
