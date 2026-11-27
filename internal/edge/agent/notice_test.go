package edgeagent

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
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
	return heldPin(t, s)
}

// heldPin opens the current pin as a handshake does, holding its file
// open until the test ends.
func heldPin(t *testing.T, s *State) pin {
	t.Helper()
	p, err := openPin(s.path(pinFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
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
	t.Cleanup(first.close)
	if err != nil || !created {
		t.Fatalf("pinFirst with nothing pinned: created %v, %v", created, err)
	}
	again, created, err := s.pinFirst(trusted)
	t.Cleanup(again.close)
	if err != nil || created || !os.SameFile(again.file, first.file) || !again.Key.Equal(first.Key) {
		t.Fatalf("pinFirst of the pinned key: %+v, created %v, %v; want the same pin, not created", again, created, err)
	}
	if _, _, err := s.pinFirst(presented); err == nil {
		t.Fatal("pinFirst replaced a pinned key")
	}
	if got, err := s.PinnedKey(); err != nil || !got.Equal(trusted) {
		t.Fatalf("pinned key %x, %v; want the one pinned first", got, err)
	}
}

// writeOlderPin writes key as an older binary's `edge trust` does: the
// key alone, by a rename, without the state lock.
func writeOlderPin(t *testing.T, s *State, key ed25519.PublicKey) {
	t.Helper()
	if err := s.write(pinFile, struct {
		Key ed25519.PublicKey `json:"key"`
	}{key}); err != nil {
		t.Fatal(err)
	}
}

// TestNoticesAcrossAnOlderBinarysRepin stores notices under a pin this
// build wrote and under one an older binary wrote, then lets an older
// binary pin another key and the first again, as after a rollback: the
// stored notices are none.
func TestNoticesAcrossAnOlderBinarysRepin(t *testing.T) {
	for _, older := range []bool{false, true} {
		s := OpenState(t.TempDir())
		a, _, _ := ed25519.GenerateKey(rand.Reader)
		b, _, _ := ed25519.GenerateKey(rand.Reader)
		p := pinNow(t, s, a)
		if older {
			writeOlderPin(t, s, a)
			p = heldPin(t, s)
		}
		if err := s.writeNotices(p, testStored()); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(noticeServerID); err != nil || len(got.Notices) != 1 {
			t.Fatalf("pin by an older binary %v: stored notices %+v, %v", older, got, err)
		}
		writeOlderPin(t, s, b)
		writeOlderPin(t, s, a)
		if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil {
			t.Fatalf("pin by an older binary %v: read %d notice(s) from before the old-format re-pin", older, len(got.Notices))
		}
	}
}

// TestOlderBinarysTrustDuringAWriteStands lets an older binary's edge
// trust pin B between this build's read of the pin A and its notices
// write: B stays pinned, its file untouched, and no notices are stored.
func TestOlderBinarysTrustDuringAWriteStands(t *testing.T) {
	s := OpenState(t.TempDir())
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	writeOlderPin(t, s, a)
	read := heldPin(t, s)
	writeOlderPin(t, s, b)
	trusted, err := os.ReadFile(s.path(pinFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeNotices(read, testStored()); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(s.path(pinFile)); err != nil || !bytes.Equal(after, trusted) {
		t.Fatalf("the pin file changed after the notices write: %s, %v", after, err)
	}
	if got, err := s.PinnedKey(); err != nil || !got.Equal(b) {
		t.Fatalf("pinned key %x, %v; want the one edge trust pinned", got, err)
	}
	if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil || noticesFileExists(t, s, a) {
		t.Fatalf("notices stored across the older binary's pin: %+v, %v", got, err)
	}
}

// TestOlderBinarysPinDuringTheLinkStoresNothing lets an older binary
// pin, the same key or another and back, after writeNotices checked the
// pin and before it links the pin file: no notices are read afterwards
// and none are left on disk.
func TestOlderBinarysPinDuringTheLinkStoresNothing(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	for name, repin := range map[string][]ed25519.PublicKey{"A to A": {a}, "A to B to A": {b, a}} {
		s := OpenState(t.TempDir())
		p := pinNow(t, s, a)
		s.beforeLink = func() {
			for _, k := range repin {
				writeOlderPin(t, s, k)
			}
		}
		if err := s.writeNotices(p, testStored()); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil {
			t.Errorf("%s: read %d notice(s) stored under the pin before", name, len(got.Notices))
		}
		if noticesFileExists(t, s, a) {
			t.Errorf("%s: notices.json left beside a link to another pin", name)
		}
		if _, err := os.Stat(s.path(keyDirFile(a, noticesPinFile))); err == nil {
			t.Errorf("%s: notices.pin left", name)
		}
	}
}

// TestFirstPinDoesNotTakeAnOlderBinarysPin lets an older binary pin
// just before pinFirst publishes its first pin: another key stays pinned
// and is refused as a changed key; the same key is used, not created, and
// the returned pin holds the file in place.
func TestFirstPinDoesNotTakeAnOlderBinarysPin(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, path := range []string{"rename without replacing", "hard link"} {
		for name, older := range map[string]ed25519.PublicKey{"another key": b, "the same key": a} {
			s := OpenState(t.TempDir())
			if path == "hard link" {
				s.renameNoReplace = func(string, string) error { return syscall.ENOSYS }
			}
			s.beforePublish = func() { writeOlderPin(t, s, older) }
			p, created, err := s.pinFirst(a)
			t.Cleanup(p.close)
			if got, kerr := s.PinnedKey(); kerr != nil || !got.Equal(older) {
				t.Fatalf("%s, %s: pinned key %x, %v; want the older binary's", path, name, got, kerr)
			}
			if !older.Equal(a) {
				if err == nil {
					t.Errorf("%s, %s: pinFirst took the older binary's pin of another key", path, name)
				}
				continue
			}
			cur, serr := os.Stat(s.path(pinFile))
			if err != nil || created || serr != nil || !os.SameFile(cur, p.file) {
				t.Errorf("%s, %s: pinFirst = created %v, %v; want the pin file in place, not created", path, name, created, err)
			}
		}
	}
}

// TestFirstPinHoldsThePinItPublished pins with pinFirst, then lets an
// older binary pin another key: the pin pinFirst returned is still the
// file it published, so notices stored under it are not stored.
func TestFirstPinHoldsThePinItPublished(t *testing.T) {
	s := OpenState(t.TempDir())
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	p, created, err := s.pinFirst(a)
	if err != nil || !created || !p.Key.Equal(a) {
		t.Fatalf("pinFirst = %x, created %v, %v", p.Key, created, err)
	}
	t.Cleanup(p.close)
	writeOlderPin(t, s, b)
	if err := s.writeNotices(p, testStored()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil || noticesFileExists(t, s, b) || noticesFileExists(t, s, a) {
		t.Fatalf("notices stored under the older binary's pin: %+v, %v", got, err)
	}
}

// withoutLinks makes s's file system offer neither a rename that
// replaces nothing nor hard links, failing the link with linkErr.
func withoutLinks(s *State, linkErr syscall.Errno) {
	s.renameNoReplace = func(string, string) error { return syscall.ENOSYS }
	s.linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: linkErr}
	}
}

// TestNoHardLinks pins and stores notices where hard links fail, as on
// vfat or a FUSE file system: the first pin falls back to a rename and
// holds the file it put in place, and no notices are stored.
func TestNoHardLinks(t *testing.T) {
	for _, linkErr := range []syscall.Errno{syscall.EPERM, syscall.EOPNOTSUPP} {
		s := OpenState(t.TempDir())
		withoutLinks(s, linkErr)
		key, _, _ := ed25519.GenerateKey(rand.Reader)
		p, created, err := s.pinFirst(key)
		if err != nil || !created || !p.Key.Equal(key) {
			t.Fatalf("%v: pinFirst without hard links = %x, created %v, %v", linkErr, p.Key, created, err)
		}
		t.Cleanup(p.close)
		if cur, err := os.Stat(s.path(pinFile)); err != nil || !os.SameFile(cur, p.file) {
			t.Fatalf("%v: the pin returned is not the pin file in place: %v", linkErr, err)
		}
		if err := s.writeNotices(p, testStored()); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil || noticesFileExists(t, s, key) {
			t.Fatalf("%v: notices stored without hard links: %+v, %v", linkErr, got, err)
		}
		leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(s.path(keyDirFile(key, noticesFile))), ".*"))
		if len(leftovers) != 0 {
			t.Errorf("%v: temporary files left: %v", linkErr, leftovers)
		}
	}
}

// TestSymlinkedPinStoresNoNotices points edge_key.json at a pin file
// elsewhere: the server pins and stores no notices, so replacing the
// link's target cannot keep notices readable.
func TestSymlinkedPinStoresNoNotices(t *testing.T) {
	s := OpenState(t.TempDir())
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	pinNow(t, s, key)
	target := filepath.Join(t.TempDir(), "edge_key.json")
	if err := os.Rename(s.path(pinFile), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.path(pinFile)); err != nil {
		t.Fatal(err)
	}
	p := heldPin(t, s)
	if !p.Key.Equal(key) {
		t.Fatalf("pin through the link %x", p.Key)
	}
	if err := s.writeNotices(p, testStored()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notices(noticeServerID); err != nil || got.Notices != nil || noticesFileExists(t, s, key) {
		t.Fatalf("notices stored under a symlinked pin: %+v, %v", got, err)
	}
}

// TestFirstPinOverADanglingLink pins where edge_key.json is a symbolic
// link to a missing file: the error says so and names the repair.
func TestFirstPinOverADanglingLink(t *testing.T) {
	refuse := func(e syscall.Errno) func(string, string) error {
		return func(oldname, newname string) error {
			return &os.LinkError{Op: "test", Old: oldname, New: newname, Err: e}
		}
	}
	for _, path := range []string{"rename without replacing", "hard link", "plain rename"} {
		s := OpenState(t.TempDir())
		switch path {
		case "hard link":
			s.renameNoReplace = refuse(syscall.ENOSYS)
		case "plain rename":
			s.renameNoReplace, s.linkFile = refuse(syscall.ENOSYS), refuse(syscall.EACCES)
		}
		if err := s.mkdir(s.dir); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "missing")
		if err := os.Symlink(missing, s.path(pinFile)); err != nil {
			t.Fatal(err)
		}
		key, _, _ := ed25519.GenerateKey(rand.Reader)
		_, _, err := s.pinFirst(key)
		want := s.path(pinFile) + " exists but names no readable pin file"
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "edge trust") || strings.Contains(err.Error(), "edge key changed") {
			t.Errorf("%s: pinFirst over a dangling link = %v", path, err)
		}
		if target, err := os.Readlink(s.path(pinFile)); err != nil || target != missing {
			t.Errorf("%s: the operator's link was replaced: %q, %v", path, target, err)
		}
	}
}

// TestNoReplaceErrorNamesTheFiles renames onto a missing directory: the
// error names both files and still matches the errno.
func TestNoReplaceErrorNamesTheFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the rename that replaces nothing is Linux's renameat2")
	}
	dir := t.TempDir()
	old, dst := filepath.Join(dir, "a"), filepath.Join(dir, "missing", "b")
	if err := os.WriteFile(old, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := renameNoReplace(old, dst)
	if err == nil || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), dst) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renameNoReplace = %v", err)
	}
}

// TestFirstPinFallbacks makes the rename that replaces nothing and the
// hard link fail in turn: a policy refusing the rename still pins by the
// link and keeps a pin that appeared meanwhile; a file system without
// either pins by a plain rename; and a real failure is an error that
// writes no pin.
func TestFirstPinFallbacks(t *testing.T) {
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	linkErr := func(e syscall.Errno) func(string, string) error {
		return func(oldname, newname string) error {
			return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: e}
		}
	}
	renameErr := func(e syscall.Errno) func(string, string) error {
		return func(oldname, newname string) error {
			return &os.LinkError{Op: "renameat2", Old: oldname, New: newname, Err: e}
		}
	}

	s := OpenState(t.TempDir())
	s.renameNoReplace = renameErr(syscall.EPERM)
	p, created, err := s.pinFirst(key)
	t.Cleanup(p.close)
	if err != nil || !created || !p.Key.Equal(key) {
		t.Fatalf("renameat2 refused with EPERM: pinFirst = created %v, %v; want a pin by hard link", created, err)
	}
	s = OpenState(t.TempDir())
	s.renameNoReplace = renameErr(syscall.EACCES)
	p, created, err = s.pinFirst(key)
	t.Cleanup(p.close)
	if err != nil || !created || !p.Key.Equal(key) {
		t.Fatalf("renameat2 refused with EACCES: pinFirst = created %v, %v; want a pin by hard link", created, err)
	}
	s = OpenState(t.TempDir())
	s.renameNoReplace = renameErr(syscall.EACCES)
	s.beforePublish = func() { writeOlderPin(t, s, other) }
	if _, _, err = s.pinFirst(key); err == nil || !strings.Contains(err.Error(), "edge key changed") {
		t.Fatalf("renameat2 refused by policy, a pin appeared meanwhile: pinFirst = %v; want the changed edge key", err)
	}
	if got, _ := s.PinnedKey(); !got.Equal(other) {
		t.Fatalf("pinned key %x; want the one that appeared meanwhile", got)
	}

	for _, e := range []syscall.Errno{syscall.EPERM, syscall.EACCES, syscall.ENOSYS} {
		s = OpenState(t.TempDir())
		s.renameNoReplace, s.linkFile = renameErr(syscall.ENOSYS), linkErr(e)
		p, created, err = s.pinFirst(key)
		t.Cleanup(p.close)
		if err != nil || !created {
			t.Fatalf("no renameat2, link fails with %v: pinFirst = created %v, %v; want the rename fallback", e, created, err)
		}
	}

	for _, e := range []syscall.Errno{syscall.EIO, syscall.ENOSPC, syscall.EDQUOT} {
		s = OpenState(t.TempDir())
		s.renameNoReplace, s.linkFile = renameErr(syscall.ENOSYS), linkErr(e)
		if _, _, err := s.pinFirst(key); !errors.Is(err, e) {
			t.Errorf("no renameat2, link fails with %v: pinFirst = %v; want that error", e, err)
		}
		if got, err := s.PinnedKey(); err != nil || got != nil {
			t.Errorf("link fails with %v: a pin was written: %x, %v", e, got, err)
		}
	}
}

// TestCrashLeftoversAreRemoved leaves a temporary notices file, as a
// crash would, and stores notices: the leftover is gone.
func TestCrashLeftoversAreRemoved(t *testing.T) {
	s := OpenState(filepath.Join(t.TempDir(), "server[1]"))
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	p := pinNow(t, s, key)
	dir := filepath.Dir(s.path(keyDirFile(key, noticesFile)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"." + noticesFile + ".123", "." + noticesFile + ".123.pin", "." + noticesFile + ".backup"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "."+noticesFile+".456"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.writeNotices(p, testStored()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+noticesFile+".") {
			kept = append(kept, e.Name())
		}
	}
	if want := []string{"." + noticesFile + ".456", "." + noticesFile + ".backup"}; !slices.Equal(kept, want) {
		t.Errorf("after the store, %v are left; want only the operator's %v", kept, want)
	}
	if got, err := s.Notices(noticeServerID); err != nil || len(got.Notices) != 1 {
		t.Errorf("notices after removing leftovers: %+v, %v", got, err)
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

// TestEnrollsWithoutHardLinks enrolls a fresh server where hard links
// fail: it pins, connects and stores no notices.
func TestEnrollsWithoutHardLinks(t *testing.T) {
	edge := newFakeEdge(t)
	list := edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test"}}
	edge.notices = func(id string, nonce []byte) (edgeproto.Notices, []byte) {
		return list, edgeproto.SignNotices(edge.priv, id, edge.origin, nonce, list)
	}
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	withoutLinks(a.state, syscall.EPERM)
	run(t, a)
	ec := edge.nextControl(t)
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
	if key, err := a.state.PinnedKey(); err != nil || !key.Equal(edge.pub) {
		t.Fatalf("pinned key %x, %v; want the edge's", key, err)
	}
	if got, err := a.Notices(); err != nil || got.Notices != nil {
		t.Fatalf("notices stored without hard links: %+v, %v", got, err)
	}
}
