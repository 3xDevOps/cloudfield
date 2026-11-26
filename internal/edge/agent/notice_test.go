package edgeagent

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestNoticesAreVerifiedAndStored enrolls at an edge whose ready carries
// notices: they are kept only when signed by the pinned key for this
// server, its origin and its nonce; they survive a restart, and each
// ready replaces them.
func TestNoticesAreVerifiedAndStored(t *testing.T) {
	edge := newFakeEdge(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	list := edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}}
	setNotices := func(f func(id string, nonce []byte) (edgeproto.Notices, []byte)) {
		edge.mu.Lock()
		defer edge.mu.Unlock()
		edge.notices = f
	}
	setNotices(func(id string, nonce []byte) (edgeproto.Notices, []byte) {
		return list, edgeproto.SignNotices(edge.priv, id, edge.origin, nonce, list)
	})
	cfg := Config{EdgeURL: edge.srv.URL, DataDir: t.TempDir(), SSH: newFakeSSH(), HostKey: newHostKey(t)}
	start := func() (*Agent, func()) {
		t.Helper()
		a, nerr := New(cfg)
		if nerr != nil {
			t.Fatal(nerr)
		}
		a.minBackoff, a.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
		stop, _ := run(t, a)
		ec := edge.nextControl(t)
		ec.send(edgeproto.Ping{})
		expect[edgeproto.Pong](t, ec)
		return a, stop
	}

	before := time.Now()
	a, stop := start()
	got, err := a.Notices()
	if err != nil || !slices.Equal(got.Notices, list) || !got.FirstUse || got.Received.Before(before) {
		t.Fatalf("notices of the enrollment that pinned the key: %+v, %v; want %+v on first use", got, err, list)
	}
	name, err := a.state.keyFile(noticesFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, serr := os.Stat(a.state.path(name)); serr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("notices file: %v %v, want mode 0600", info, serr)
	}
	stop()

	a, stop = start()
	if got, err = a.Notices(); err != nil || !slices.Equal(got.Notices, list) || got.FirstUse {
		t.Fatalf("notices of a later enrollment: %+v, %v; want %+v, not on first use", got, err, list)
	}
	stop()
	if got, err = OpenState(cfg.DataDir).Notices(); err != nil || !slices.Equal(got.Notices, list) {
		t.Fatalf("notices after the server stopped: %+v, %v", got, err)
	}

	more := append(slices.Clone(list), edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://edge0.example.test", Replacement: "https://edge2.example.test"})
	for _, tc := range []struct {
		name     string
		sent     edgeproto.Notices
		signed   []edgeproto.Notice
		key      ed25519.PrivateKey
		serverID string
		origin   string
		nonce    []byte
		unsigned bool
	}{
		{name: "another key", sent: list, signed: list, key: other},
		{name: "another nonce", sent: list, signed: list, nonce: make([]byte, edgeproto.NonceSize)},
		{name: "another server", sent: list, signed: list, serverID: "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "another origin", sent: list, signed: list, origin: "https://edge2.example.test"},
		{name: "an entry added", sent: more, signed: list},
		{name: "no signature", sent: list, unsigned: true},
		{name: "none sent", unsigned: true},
		{name: "a signed none"},
	} {
		setNotices(func(id string, nonce []byte) (edgeproto.Notices, []byte) {
			return list, edgeproto.SignNotices(edge.priv, id, edge.origin, nonce, list)
		})
		_, stop = start()
		stop()
		setNotices(func(id string, nonce []byte) (edgeproto.Notices, []byte) {
			if tc.unsigned {
				return tc.sent, nil
			}
			key := edge.priv
			if tc.key != nil {
				key = tc.key
			}
			if tc.nonce != nil {
				nonce = tc.nonce
			}
			return tc.sent, edgeproto.SignNotices(key, cmp.Or(tc.serverID, id), cmp.Or(tc.origin, edge.origin), nonce, tc.signed)
		})
		a, stop = start()
		if got, err := a.Notices(); err != nil || got.Notices != nil {
			t.Errorf("%s: notices %+v, %v; want the stored ones replaced by none", tc.name, got, err)
		}
		stop()
	}
}

// TestNoticesDoNotSurviveAPin checks that pinning a key, even the same
// one again, forgets its stored notices.
func TestNoticesDoNotSurviveAPin(t *testing.T) {
	s := OpenState(t.TempDir())
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	n := StoredNotices{Received: time.Now().UTC(), Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test", Replacement: "https://edge2.example.test"}}}
	for _, again := range []ed25519.PublicKey{key, other} {
		if err := s.Pin(key); err != nil {
			t.Fatal(err)
		}
		if err := s.writeNotices(key, n); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(); err != nil || !slices.Equal(got.Notices, n.Notices) {
			t.Fatalf("stored notices %+v, %v", got, err)
		}
		if err := s.Pin(again); err != nil {
			t.Fatal(err)
		}
		if err := s.Pin(key); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(); err != nil || got.Notices != nil {
			t.Fatalf("notices after pinning %x: %+v, %v", again[:4], got, err)
		}
	}
}

// TestNoticesOfAnEarlierPinAreNotWritten writes, after another key was
// pinned, notices verified under the key before: the new key's store
// stays empty, and so does the first key's once pinned again.
func TestNoticesOfAnEarlierPinAreNotWritten(t *testing.T) {
	s := OpenState(t.TempDir())
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	n := StoredNotices{Received: time.Now().UTC(), Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test", Replacement: "https://edge2.example.test"}}}
	if err := s.Pin(b); err != nil {
		t.Fatal(err)
	}
	if err := s.writeNotices(b, n); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(a); err != nil {
		t.Fatal(err)
	}
	// The enrollment verified under a; edge trust pins b before its write.
	if err := s.Pin(b); err != nil {
		t.Fatal(err)
	}
	for _, late := range []StoredNotices{n, {Received: n.Received}} {
		if err := s.writeNotices(a, late); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(); err != nil || got.Notices != nil {
			t.Fatalf("store of the key pinned since, after a late write of %d notices: %+v, %v", len(late.Notices), got, err)
		}
	}
	if err := s.Pin(a); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices(); err != nil || got.Notices != nil {
		t.Fatalf("store of the first key pinned again: %+v, %v", got, err)
	}
}

// TestDialErrorTextIsPrintable refuses a dial with text that would drive
// a terminal or forge a log line, and with too much of it.
func TestDialErrorTextIsPrintable(t *testing.T) {
	for raw, want := range map[string]string{
		"x\x1b[2J\r\nfake: connected": "x?[2J??fake: connected",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(edgeproto.ErrorBody{Error: raw})
		}))
		_, _, err := dial(t.Context(), srv.URL+edgeproto.PathServerControl, nil, handshakeTimeout)
		srv.Close()
		var de *DialError
		if !errors.As(err, &de) || de.Message != want {
			t.Errorf("refusal %.20q: %v", raw, err)
		}
	}
}

func TestPrintableTextIsCapped(t *testing.T) {
	if got := printableText(strings.Repeat("a", 5000), maxDialMessage); got != strings.Repeat("a", maxDialMessage)+"..." {
		t.Errorf("printableText of 5000 bytes is %d bytes", len(got))
	}
	if got := printableText(strings.Repeat("é", 600), 1023); !strings.HasSuffix(got, "é...") || len(got) != 1022+3 {
		t.Errorf("printableText split a rune: %q", got[len(got)-8:])
	}
}

// TestControlDialRefusalIsTyped checks that a refused upgrade reports its
// HTTP status as a *DialError, and that a redirect is not followed.
func TestControlDialRefusalIsTyped(t *testing.T) {
	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"https://old.example.test is retired"}`))
	}))
	t.Cleanup(srv.Close)
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(moved.Close)
	_, _, err := dial(t.Context(), srv.URL+edgeproto.PathServerControl, nil, handshakeTimeout)
	var de *DialError
	if !errors.As(err, &de) || de.Status != http.StatusGone || de.Message != "https://old.example.test is retired" {
		t.Fatalf("dial a 410: %v", err)
	}
	if want := "dial " + srv.URL + edgeproto.PathServerControl + ": 410 Gone: https://old.example.test is retired"; err.Error() != want {
		t.Errorf("dial a 410: %q, want %q", err, want)
	}
	_, _, err = dial(t.Context(), moved.URL+edgeproto.PathServerControl, nil, handshakeTimeout)
	if !errors.As(err, &de) || de.Status != http.StatusFound || followed.Load() || de.Message != "Location: "+elsewhere.URL+edgeproto.PathServerControl {
		t.Fatalf("dial a redirect: %v, followed %v", err, followed.Load())
	}
	if _, err = FetchEdgeInfo(t.Context(), moved.URL); err == nil || followed.Load() || !strings.Contains(err.Error(), "302 Found") {
		t.Fatalf("edge info behind a redirect: %v, followed %v", err, followed.Load())
	}
}
