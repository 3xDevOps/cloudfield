package edgeagent

import (
	"bufio"
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
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
	if got, err = OpenState(cfg.DataDir).Notices(a.serverID); err != nil || !slices.Equal(got.Notices, list) {
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

const noticeServerID = "7ozstx3hvprfzhna3cakjwhdnw"

func testStored() StoredNotices {
	return StoredNotices{ServerID: noticeServerID, Received: time.Now().UTC(),
		Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test", Replacement: "https://edge2.example.test"}}}
}

// pinNow pins key and returns the pin a handshake would read.
func pinNow(t *testing.T, s *State, key ed25519.PublicKey) pin {
	t.Helper()
	if err := s.Pin(key); err != nil {
		t.Fatal(err)
	}
	p, err := s.pinned()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noticesFileExists(t *testing.T, s *State, key ed25519.PublicKey) bool {
	t.Helper()
	_, err := os.Stat(s.path(keyDirFile(key, noticesFile)))
	return err == nil
}

// TestNoticesDoNotSurviveAPin checks that pinning a key, the same one
// again or another, deletes the notices of the key it replaces and of the
// key it pins.
func TestNoticesDoNotSurviveAPin(t *testing.T) {
	s := OpenState(t.TempDir())
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	n := testStored()
	for _, again := range []ed25519.PublicKey{key, other} {
		if err := s.writeNotices(pinNow(t, s, key), n); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(noticeServerID); err != nil || !slices.Equal(got.Notices, n.Notices) {
			t.Fatalf("stored notices %+v, %v", got, err)
		}
		pinNow(t, s, again)
		if noticesFileExists(t, s, key) {
			t.Fatalf("pinning %x left the notices of the key it replaced", again[:4])
		}
		pinNow(t, s, key)
		if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil {
			t.Fatalf("notices after pinning %x: %+v, %v", again[:4], got, err)
		}
	}
}

// TestNoticesOfAnEarlierPinAreNotWritten writes notices verified under a
// pin after the state was pinned again: nothing is written, the first
// key's file does not come back even once that key is pinned again, and
// a late empty write does not delete the current pin's notices.
func TestNoticesOfAnEarlierPinAreNotWritten(t *testing.T) {
	s := OpenState(t.TempDir())
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	n := testStored()

	verified := pinNow(t, s, a)
	pinNow(t, s, b)
	pinNow(t, s, a)
	if err := s.writeNotices(verified, n); err != nil {
		t.Fatal(err)
	}
	if noticesFileExists(t, s, a) {
		t.Fatal("a late write under an earlier pin of the same key stored its notices")
	}

	stale := pinNow(t, s, a)
	current := pinNow(t, s, b)
	if err := s.writeNotices(current, n); err != nil {
		t.Fatal(err)
	}
	if err := s.writeNotices(stale, StoredNotices{ServerID: noticeServerID, Received: n.Received}); err != nil {
		t.Fatal(err)
	}
	if err := s.writeNotices(stale, n); err != nil {
		t.Fatal(err)
	}
	if noticesFileExists(t, s, a) {
		t.Fatal("a late write under an earlier pin stored notices for its key")
	}
	if got, err := s.Notices(noticeServerID); err != nil || !slices.Equal(got.Notices, n.Notices) {
		t.Fatalf("a late write under an earlier pin changed the current pin's notices: %+v, %v", got, err)
	}
}

// TestNoticesOfAnotherServerID checks that notices stored for one server
// id are none for another, as after the host key changed.
func TestNoticesOfAnotherServerID(t *testing.T) {
	s := OpenState(t.TempDir())
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := s.writeNotices(pinNow(t, s, key), testStored()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices("aaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil || got.Notices != nil {
		t.Fatalf("notices for another server id: %+v, %v", got, err)
	}
}

// TestFirstPinKeepsAKeyPinnedMeanwhile pins a key with pinFirst after
// another was pinned since the caller found none: it is refused, and the
// pinned key stays.
func TestFirstPinKeepsAKeyPinnedMeanwhile(t *testing.T) {
	s := OpenState(t.TempDir())
	trusted, _, _ := ed25519.GenerateKey(rand.Reader)
	presented, _, _ := ed25519.GenerateKey(rand.Reader)
	first, created, err := s.pinFirst(trusted)
	if err != nil || !created {
		t.Fatalf("pinFirst with nothing pinned: created %v, %v", created, err)
	}
	if again, created, err := s.pinFirst(trusted); err != nil || created || again.ID != first.ID || !again.Key.Equal(first.Key) {
		t.Fatalf("pinFirst of the pinned key: %+v, created %v, %v; want the same pin, not created", again, created, err)
	}
	if _, _, err := s.pinFirst(presented); err == nil {
		t.Fatal("pinFirst replaced a pinned key")
	}
	if got, err := s.PinnedKey(); err != nil || !got.Equal(trusted) {
		t.Fatalf("pinned key %x, %v; want the one pinned first", got, err)
	}
}

// writeLegacyPin writes key as a build before pin ids, or an older
// binary's edge trust, does: the key alone.
func writeLegacyPin(t *testing.T, s *State, key ed25519.PublicKey) {
	t.Helper()
	if err := s.write(pinFile, struct {
		Key ed25519.PublicKey `json:"key"`
	}{key}); err != nil {
		t.Fatal(err)
	}
}

// TestNoticesAcrossAnOlderBinarysRepin stores notices, then lets an older
// binary pin another key and the first again without an id, as after a
// rollback: the stored notices are none until a new enrollment stores
// its own under a pin with an id.
func TestNoticesAcrossAnOlderBinarysRepin(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		s := OpenState(t.TempDir())
		a, _, _ := ed25519.GenerateKey(rand.Reader)
		b, _, _ := ed25519.GenerateKey(rand.Reader)
		p := pinNow(t, s, a)
		if legacyFirst {
			writeLegacyPin(t, s, a)
			if err := s.writeNotices(pin{Key: a}, testStored()); err == nil {
				t.Fatal("notices stored under a pin without an id")
			}
			var err error
			if p, err = s.identify(pin{Key: a}); err != nil || p.ID == "" {
				t.Fatalf("identify a legacy pin: %+v, %v", p, err)
			}
		}
		if err := s.writeNotices(p, testStored()); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(noticeServerID); err != nil || len(got.Notices) != 1 {
			t.Fatalf("legacy first %v: stored notices %+v, %v", legacyFirst, got, err)
		}
		writeLegacyPin(t, s, b)
		writeLegacyPin(t, s, a)
		if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil {
			t.Fatalf("legacy first %v: read %d notice(s) from before the old-format re-pin", legacyFirst, len(got.Notices))
		}
		if _, err := s.identify(pin{Key: b}); err == nil {
			t.Fatal("identify gave an id to a pin of another key")
		}
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

func TestPrintableText(t *testing.T) {
	if got := printableText(strings.Repeat("a", 5000), maxDialMessage); got != strings.Repeat("a", maxDialMessage-3)+"..." {
		t.Errorf("printableText of 5000 bytes is %d bytes, want %d", len(got), maxDialMessage)
	}
	if got := printableText(strings.Repeat("é", 600), 1023); !strings.HasSuffix(got, "é...") || len(got) > 1023 {
		t.Errorf("printableText of 1200 bytes capped at 1023 is %d bytes ending %q", len(got), got[len(got)-8:])
	}
	if got := printableText("ok\u202egnp.exe\u2028fake: connected\u2029\u200b", 100); got != "ok?gnp.exe?fake: connected??" {
		t.Errorf("printableText kept a format character or separator: %q", got)
	}
}

// TestEdgeInfoRefusalIgnoresTheReasonPhrase refuses GET /v1/edge with a
// status line whose reason phrase would drive a terminal: the error names
// the status by its number and standard text only.
func TestEdgeInfoRefusalIgnoresTheReasonPhrase(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c.Close() //nolint:errcheck // test server
		_, _ = http.ReadRequest(bufio.NewReader(c))
		_, _ = io.WriteString(c, "HTTP/1.1 410 \x1b[2Jfake: connected\u2028x\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}()
	_, err = FetchEdgeInfo(t.Context(), "http://"+ln.Addr().String())
	if err == nil || !strings.HasSuffix(err.Error(), ": 410 Gone ") || strings.ContainsAny(err.Error(), "\x1b\u2028") {
		t.Fatalf("FetchEdgeInfo = %q", err)
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
	if _, err = FetchEdgeInfo(t.Context(), srv.URL); err == nil || !strings.HasSuffix(err.Error(), "410 Gone https://old.example.test is retired") {
		t.Fatalf("edge info refused: %v", err)
	}
	if _, err = FetchEdgeInfo(t.Context(), moved.URL); err == nil || followed.Load() || !strings.Contains(err.Error(), "302 Found") {
		t.Fatalf("edge info behind a redirect: %v, followed %v", err, followed.Load())
	}
}
