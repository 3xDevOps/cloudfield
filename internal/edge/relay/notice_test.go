package relay

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
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
	_, err := New(t.Context(), Config{Origin: "https://edge.example.test:0443", MoreOrigins: []string{"https://edge2.example.test"},
		EdgeKey: key, Directory: &fakeDir{}, Egress: &fakeEgress{months: map[string]int64{}},
		Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: "https://edge.example.test:0443", Replacement: "https://edge2.example.test"}}})
	if err == nil || !strings.Contains(err.Error(), "without a leading zero in the port") {
		t.Errorf("New with a notice for a zero-padded port = %v", err)
	}
}

// TestRetiredNoticesNameNoServedOrigin refuses a retired notice for a
// served relay origin or with a replacement that is not one, and more
// notices than a ready carries; it accepts a retired origin the edge no
// longer serves.
func TestRetiredNoticesNameNoServedOrigin(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	newWith := func(notices ...edgeproto.Notice) error {
		r, err := New(t.Context(), Config{Origin: "https://edge.example.test", EdgeKey: key, Directory: &fakeDir{},
			Egress: &fakeEgress{months: map[string]int64{}}, Notices: notices})
		if r != nil {
			_ = r.Shutdown(t.Context())
		}
		return err
	}
	for _, tt := range []struct {
		n    edgeproto.Notice
		want string
	}{
		{edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://edge.example.test", Replacement: "https://edge.example.test"},
			"relay: the retired notice for https://edge.example.test names https://edge.example.test: https://edge.example.test must not be a relay origin of this edge, and https://edge.example.test must be one"},
		{edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://old.example.test", Replacement: "https://edge9.example.test"},
			"relay: the retired notice for https://old.example.test names https://edge9.example.test: https://old.example.test must not be a relay origin of this edge, and https://edge9.example.test must be one"},
	} {
		if err := newWith(tt.n); err == nil || err.Error() != tt.want {
			t.Errorf("New with %+v = %v, want %q", tt.n, err, tt.want)
		}
	}
	ok := edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://old.example.test", Replacement: "https://edge.example.test"}
	if err := newWith(ok); err != nil {
		t.Errorf("New with %+v = %v", ok, err)
	}
	if err := newWith(slices.Repeat([]edgeproto.Notice{ok}, edgeproto.MaxRelayOrigins+1)...); err == nil || err.Error() != "relay: 17 notices, limit 16" {
		t.Errorf("New with 17 notices = %v", err)
	}
}

// TestRelayedAnswerCarriesTheEdgesNotice forwards a connector request to
// a server that answers with its own Aether-Edge-Retiring: that value
// never passes, and the answer carries the edge's when the relay origin
// is retiring.
func TestRelayedAnswerCarriesTheEdgesNotice(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	serveData(t, a, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"+edgeproto.HeaderRetiring+": https://evil.example.test\r\nContent-Length: 2\r\n\r\n{}")
	get := func() []string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+edgeproto.MCPPath(a.id), strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("answer %s", resp.Status)
		}
		return resp.Header.Values(edgeproto.HeaderRetiring)
	}
	if got := get(); got != nil {
		t.Errorf("nothing retiring: the answer carries %s %q", edgeproto.HeaderRetiring, got)
	}
	e.r.notices = []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}}
	if got := get(); !slices.Equal(got, []string{"https://edge2.example.test; date=2027-03-01"}) {
		t.Errorf("retiring: the answer carries %s %q, want the edge's value alone", edgeproto.HeaderRetiring, got)
	}
}

// TestRelayFailureAfterEarlyHintsCarriesTheEdgesNotice forwards a
// connector request to a server that sends a 1xx answer and then drops
// the connection: the edge's own error answer still carries its notice.
func TestRelayFailureAfterEarlyHintsCarriesTheEdgesNotice(t *testing.T) {
	e := newEnv(t)
	e.r.notices = []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: e.origin, Replacement: "https://edge2.example.test"}}
	a := claimedAgent(t, e)
	go func() {
		select {
		case nc := <-a.data:
			_, _ = http.ReadRequest(bufio.NewReader(nc))
			_, _ = io.WriteString(nc, "HTTP/1.1 103 Early Hints\r\nLink: </x.css>; rel=preload\r\n\r\n")
			_ = nc.Close()
		case <-t.Context().Done():
		}
	}()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+edgeproto.MCPPath(a.id), strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 500 {
		t.Fatalf("answer %s, want the edge's error", resp.Status)
	}
	if got := resp.Header.Values(edgeproto.HeaderRetiring); !slices.Equal(got, []string{"https://edge2.example.test"}) {
		t.Errorf("the edge's error answer carries %s %q", edgeproto.HeaderRetiring, got)
	}
}
