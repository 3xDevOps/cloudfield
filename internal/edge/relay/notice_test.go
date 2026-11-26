package relay

import (
	"bytes"
	"crypto/ed25519"
	"slices"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestReadyCarriesSignedNotices enrolls claimed servers at a relay with one
// retiring origin: a server that sends a notice nonce gets the notices
// signed for it, its origin and its nonce; one that sends none gets a
// ready without them.
func TestReadyCarriesSignedNotices(t *testing.T) {
	e := newEnv(t)
	pub := e.edgeKey.Public().(ed25519.PublicKey)
	enrollWith := func(nonce []byte) edgeproto.Ready {
		t.Helper()
		signer := newSigner(t)
		e.dir.mu.Lock()
		e.dir.owners[edgeproto.ServerID(signer.PublicKey())] = true
		e.dir.mu.Unlock()
		ws := dialControl(t, e)
		c := challenge(t, ws)
		hello := helloFor(t, signer, e.origin, c.Nonce)
		hello.NoticeNonce = nonce
		send(t, ws, hello)
		m, err := recv(t.Context(), ws)
		if err != nil {
			t.Fatal(err)
		}
		return m.(edgeproto.Ready)
	}
	nonce := bytes.Repeat([]byte{7}, edgeproto.NonceSize)

	r := enrollWith(nonce)
	if r.Notices != nil || edgeproto.VerifyNotices(pub, r.ServerID, e.origin, nonce, nil, r.NoticesSignature) != nil {
		t.Fatalf("ready with nothing retiring: notices %+v, signature %x; want a signed empty list", r.Notices, r.NoticesSignature)
	}

	want := []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}}
	e.r.notices = want
	r = enrollWith(nonce)
	if !slices.Equal(r.Notices, want) {
		t.Fatalf("ready carries notices %+v, want %+v", r.Notices, want)
	}
	if err := edgeproto.VerifyNotices(pub, r.ServerID, e.origin, nonce, r.Notices, r.NoticesSignature); err != nil {
		t.Fatalf("notices do not verify for this server, origin and nonce: %v", err)
	}
	other := enrollWith(nonce)
	if edgeproto.VerifyNotices(pub, r.ServerID, e.origin, nonce, other.Notices, other.NoticesSignature) == nil {
		t.Error("the notices signed for another server verify for this one")
	}
	for _, n := range [][]byte{nil, nonce[:16]} {
		if r := enrollWith(n); r.Notices != nil || r.NoticesSignature != nil {
			t.Errorf("hello with notice nonce %x: ready carries notices %+v, signature %x", n, r.Notices, r.NoticesSignature)
		}
	}
}

func TestNoticesNameRelayOrigins(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	for _, n := range []edgeproto.Notice{
		{State: edgeproto.NoticeRetiring, Origin: "https://edge3.example.test", Replacement: "https://edge2.example.test"},
		{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test", Replacement: "https://edge3.example.test"},
	} {
		_, err := New(t.Context(), Config{Origin: "https://edge.example.test", MoreOrigins: []string{"https://edge2.example.test"},
			EdgeKey: key, Directory: &fakeDir{}, Egress: &fakeEgress{months: map[string]int64{}}, Notices: []edgeproto.Notice{n}})
		if err == nil || !strings.Contains(err.Error(), "both must be relay origins of this edge") {
			t.Errorf("New with the notice %+v = %v", n, err)
		}
	}
}
