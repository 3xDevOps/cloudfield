package edgeagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"reflect"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestNoticesAreVerifiedAndStored enrolls at an edge whose ready carries
// one notice signed by its key for this handshake, and others that are
// not: only the first is kept, it survives a restart, and it is gone
// once the edge stops sending it.
func TestNoticesAreVerifiedAndStored(t *testing.T) {
	edge := newFakeEdge(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	retiring := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}
	edge.notices = func(id string, nonce []byte) edgeproto.Notices {
		otherNonce := make([]byte, edgeproto.NonceSize)
		retired := edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://edge0.example.test", Replacement: "https://edge2.example.test"}
		forged := edgeproto.SignNotice(edge.priv, id, nonce, retired)
		forged.Replacement = "https://evil.example.test"
		return edgeproto.Notices{
			edgeproto.SignNotice(other, id, nonce, retired),
			edgeproto.SignNotice(edge.priv, id, otherNonce, retired),
			edgeproto.SignNotice(edge.priv, "aaaaaaaaaaaaaaaaaaaaaaaaaa", nonce, retired),
			forged,
			edgeproto.SignNotice(edge.priv, id, nonce, retiring),
		}
	}
	cfg := Config{EdgeURL: edge.srv.URL, DataDir: t.TempDir(), SSH: newFakeSSH(), HostKey: newHostKey(t)}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.minBackoff, a.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	if got, err := a.Notices(); got != nil || err != nil {
		t.Fatalf("notices before enrolling: %+v, %v", got, err)
	}
	stop, _ := run(t, a)
	ec := edge.nextControl(t)
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
	got, err := a.Notices()
	if err != nil || len(got) != 1 {
		t.Fatalf("notices %+v, %v; want the one verified notice", got, err)
	}
	got[0].Signature = nil
	if !reflect.DeepEqual(got[0], retiring) {
		t.Fatalf("stored notice %+v, want %+v", got[0], retiring)
	}
	name, err := a.state.keyFile(noticesFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(a.state.path(name)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("notices file: %v %v, want mode 0600", info, err)
	}
	stop()

	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.Notices(); err != nil || len(got) != 1 || got[0].Origin != retiring.Origin {
		t.Fatalf("notices after a restart: %+v, %v", got, err)
	}

	edge.mu.Lock()
	edge.notices = nil
	edge.mu.Unlock()
	restarted.minBackoff, restarted.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	run(t, restarted)
	ec = edge.nextControl(t)
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
	if got, err := restarted.Notices(); got != nil || err != nil {
		t.Fatalf("notices once the edge sends none: %+v, %v", got, err)
	}
}

// TestNoticesFollowThePinnedKey checks that notices are kept per edge
// key: pinning another key shows none of the first edge's.
func TestNoticesFollowThePinnedKey(t *testing.T) {
	s := OpenState(t.TempDir())
	first, _, _ := ed25519.GenerateKey(rand.Reader)
	second, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := s.Pin(first); err != nil {
		t.Fatal(err)
	}
	n := []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test", Replacement: "https://edge2.example.test"}}
	if err := s.writeNotices(n); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(second); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices(); got != nil || err != nil {
		t.Fatalf("notices under another key: %+v, %v", got, err)
	}
	if err := s.Pin(first); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices(); err != nil || !reflect.DeepEqual(got, n) {
		t.Fatalf("notices of the first key pinned again: %+v, %v", got, err)
	}
}
