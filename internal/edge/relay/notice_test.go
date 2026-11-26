package relay

import (
	"crypto/ed25519"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestReadyCarriesSignedNotices enrolls a server at a relay with one
// retiring origin: its ready holds that origin's notice, signed for this
// server and this handshake.
func TestReadyCarriesSignedNotices(t *testing.T) {
	e := newEnv(t)
	if a := enroll(t, e, newSigner(t)); a.ready.Notices != nil {
		t.Fatalf("ready with nothing retiring carries notices %+v", a.ready.Notices)
	}
	want := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}
	e.r.notices = []edgeproto.Notice{want}

	signer := newSigner(t)
	ws := dialControl(t, e)
	c := challenge(t, ws)
	send(t, ws, helloFor(t, signer, e.origin, c.Nonce))
	m, err := recv(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	ready := m.(edgeproto.Ready)
	if len(ready.Notices) != 1 {
		t.Fatalf("ready carries notices %+v, want one", ready.Notices)
	}
	got := ready.Notices[0]
	pub := e.edgeKey.Public().(ed25519.PublicKey)
	if err := edgeproto.VerifyNotice(pub, ready.ServerID, c.Nonce, got); err != nil {
		t.Fatalf("notice does not verify for this server and handshake: %v", err)
	}
	if got.State != want.State || got.Origin != want.Origin || got.Replacement != want.Replacement || got.Date != want.Date {
		t.Errorf("notice %+v, want %+v", got, want)
	}
	other := enroll(t, e, newSigner(t))
	if edgeproto.VerifyNotice(pub, ready.ServerID, c.Nonce, other.ready.Notices[0]) == nil {
		t.Error("the notice of another server verifies for this one")
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
