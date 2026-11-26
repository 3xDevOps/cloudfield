package edgetest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
)

// TestRetiringPair serves two pairs with the first, A, retiring in
// favour of the second, B. A server enrolled at A stores the notice its
// edge signed, and a client at A sees it on every answer; a server at B
// stores it too, since its ready lists every retiring relay origin, and
// a client at B sees none.
func TestRetiringPair(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	signinA, relayA, signinB, relayB := h.signinURL, h.relayURL, h.signinB, h.relayB
	h.stopEdge()
	h.retiring = []edge.Retiring{{RelayOrigin: relayA, Replacement: relayB, Date: "2027-03-01"}}
	h.startEdge()
	want := edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: relayA, Replacement: relayB, Date: "2027-03-01"}

	from := h.proxy.mark()
	a := h.newServer(edgeproto.PolicyAccount)
	if got := h.readyTo(t, a, from).Notices; !slices.Equal(got, edgeproto.Notices{want}) {
		t.Fatalf("ready to the server at A carries notices %+v, want %+v", got, want)
	}
	if got, err := a.agent.Notices(); err != nil || !slices.Equal(got.Notices, []edgeproto.Notice{want}) || !got.FirstUse {
		t.Fatalf("server at A stored notices %+v, %v; want %+v on first use", got, err, want)
	}
	alA := h.login(alice)[0]
	h.claimServer(alA, a)
	retiring := edgeclient.Retiring{Replacement: relayB, Date: "2027-03-01"}
	if _, err := alA.edge.Servers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := alA.edge.Retiring(); !ok || got != retiring {
		t.Errorf("client at A after an API answer: %+v, %v; want %+v", got, ok, retiring)
	}
	if got, ok := dialFresh(t, alA, relayA, a.id); !ok || got != retiring {
		t.Errorf("client at A after a relayed connection: %+v, %v; want %+v", got, ok, retiring)
	}
	if got, ok, nerr := alA.edge.RetiringNotice(context.Background()); nerr != nil || !ok || got != retiring {
		t.Errorf("client at A from %s: %+v, %v, %v", edgeproto.PathEdgeInfo, got, ok, nerr)
	}

	h.signinURL, h.relayURL = signinB, relayB
	from = h.proxy.mark()
	b := h.newServer(edgeproto.PolicyAccount)
	if got := h.readyTo(t, b, from).Notices; !slices.Equal(got, edgeproto.Notices{want}) {
		t.Fatalf("ready to the server at B carries notices %+v, want the one for A", got)
	}
	if got, nerr := b.agent.Notices(); nerr != nil || !slices.Equal(got.Notices, []edgeproto.Notice{want}) {
		t.Fatalf("server at B stored notices %+v, %v; want the one for A", got, nerr)
	}
	alB := h.login(alice)[0]
	h.claimServer(alB, b)
	if _, err := alB.edge.Servers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := alB.edge.Retiring(); ok {
		t.Errorf("client at B sees a notice %+v", got)
	}
	if got, ok := dialFresh(t, alB, relayB, b.id); ok {
		t.Errorf("client at B sees a notice on a relayed connection %+v", got)
	}
	if got, ok, err := alB.edge.RetiringNotice(context.Background()); err != nil || ok {
		t.Errorf("client at B from %s: %+v, %v, %v", edgeproto.PathEdgeInfo, got, ok, err)
	}

	// Each answer of A differs from B's by the header alone.
	pages := []struct{ a, b, path string }{{signinA, signinB, "/signin"}, {relayA, relayB, edgeproto.PathEdgeInfo}}
	for _, p := range pages {
		got, other := headerNames(t, p.a+p.path), headerNames(t, p.b+p.path)
		if want := slices.Sorted(slices.Values(append(other, edgeproto.HeaderRetiring))); !slices.Equal(got, want) {
			t.Errorf("%s%s answers with %v, want %v: %s%s's and %s", p.a, p.path, got, want, p.b, p.path, edgeproto.HeaderRetiring)
		}
	}

	// Unset again, the edge sends a server that sends no notice nonce what
	// it sent before notices existed, and one that does a signed empty list.
	h.stopEdge()
	h.retiring = nil
	h.startEdge()
	eventually(t, "the servers forget the notice", func() error {
		for _, s := range []*serverNode{a, b} {
			if got, err := s.agent.Notices(); err != nil || got.Notices != nil {
				return fmt.Errorf("server %s holds notices %+v, %v", s.id, got, err)
			}
		}
		return nil
	})
	for _, origin := range []string{relayA, relayB} {
		raw, ready := rawReady(t, origin, nil)
		old, err := edgeproto.EncodeControl(edgeproto.Ready{ServerID: ready.ServerID, State: ready.State, EdgeKey: ready.EdgeKey, RelayOrigins: ready.RelayOrigins})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, old) {
			t.Errorf("ready at %s with nothing retiring:\n%s\nwant\n%s", origin, raw, old)
		}
		nonce := bytes.Repeat([]byte{9}, edgeproto.NonceSize)
		if _, ready = rawReady(t, origin, nonce); ready.Notices != nil ||
			edgeproto.VerifyNotices(ready.EdgeKey, ready.ServerID, origin, nonce, nil, ready.NoticesSignature) != nil {
			t.Errorf("ready at %s to a server that sent a notice nonce: notices %+v, signature %x; want a signed empty list", origin, ready.Notices, ready.NoticesSignature)
		}
	}
	for _, p := range pages {
		if got, other := headerNames(t, p.a+p.path), headerNames(t, p.b+p.path); !slices.Equal(got, other) || slices.Contains(got, edgeproto.HeaderRetiring) {
			t.Errorf("with nothing retiring %s%s answers with %v, %s%s with %v", p.a, p.path, got, p.b, p.path, other)
		}
	}
}

// dialFresh opens a relayed connection to serverID from a new client of
// c's install at relay, which has seen no other answer, and returns the
// notice of the edge's answer.
func dialFresh(t *testing.T, c *client, relay, serverID string) (edgeclient.Retiring, bool) {
	t.Helper()
	fresh, err := edgeclient.New(c.dir, relay)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := fresh.Dial(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	return fresh.Retiring()
}

// headerNames are the sorted names of the headers of the answer to GET
// url.
func headerNames(t *testing.T, url string) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	return slices.Sorted(maps.Keys(resp.Header))
}

// rawReady enrolls a new server at origin by hand, with nonce as its
// hello's notice nonce, and returns the ready the edge sent, as bytes and
// decoded.
func rawReady(t *testing.T, origin string, nonce []byte) ([]byte, edgeproto.Ready) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(origin, "http")+edgeproto.PathServerControl, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow() //nolint:errcheck // test
	_, data, err := ws.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, err := edgeproto.DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}
	ch := m.(edgeproto.Challenge)
	sig, err := edgeproto.SignEnrollment(signer, origin, ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := edgeproto.EncodeControl(edgeproto.Hello{Version: edgeproto.Version, HostKey: signer.PublicKey().Marshal(), Signature: sig,
		AgentVersion: "test", Name: "raw", AccessPolicy: edgeproto.PolicyAccount, NoticeNonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	if err = ws.Write(t.Context(), websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	_, raw, err := ws.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, err = edgeproto.DecodeControl(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, m.(edgeproto.Ready)
}
