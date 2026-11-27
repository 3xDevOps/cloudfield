package edgetest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/edge/relay"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
	"github.com/3xDevOps/cloudfield/internal/version"
)

// An edge that starts serving a second origin pair keeps serving the
// server and the client of its first, and a server enrolled at either
// pair is reached by a client signed in at the other: every grant names
// the relay origin its server enrolled at, which the server checks,
// whichever relay origin the client came in on. The server's agent reads
// nothing of the list of relay origins the edge now sends.
func TestOriginPairs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	signinA, relayA := h.signinURL, h.relayURL
	from := h.proxy.mark()
	a := h.newServer(edgeproto.PolicyAccount)
	ready := h.readyTo(t, a, from)
	if ready.RelayOrigins != nil {
		t.Fatalf("ready from an edge with one pair lists relay origins %v", ready.RelayOrigins)
	}
	alA := h.login(alice)[0]
	h.claimServer(alA, a)

	h.stopEdge()
	h.pairB, h.recordOrigins = true, true
	from = h.proxy.mark()
	h.startEdge()
	signinB, relayB := h.signinB, h.relayB
	eventually(t, "the first pair's server back on the edge", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	if got := h.readyTo(t, a, from).RelayOrigins; !slices.Equal(got, []string{relayA, relayB}) {
		t.Fatalf("ready lists relay origins %v, want %s and %s", got, relayA, relayB)
	}
	h.mustDial(alA, a)

	h.signinURL, h.relayURL = signinB, relayB
	b := h.newServer(edgeproto.PolicyAccount)
	alB := h.login(alice)[0]
	if s, err := alB.edge.Session(); err != nil || s.SigninOrigin != signinB {
		t.Fatalf("session of the install signed in at %s: %+v, %v", relayB, s, err)
	}

	// Alice, signed in at the second pair, reaches the first pair's server
	// through the second pair's relay origin, and claims and reaches the
	// second pair's server.
	dialVia(t, h, alB, a, relayB, relayA)
	h.claimServer(alB, b)
	dialVia(t, h, alB, b, relayB, relayB)
	// Alice's first install, signed in at the first pair, reaches the
	// second pair's server through the first pair's relay origin.
	dialVia(t, h, alA, b, relayA, relayB)

	// A connector request is counted by the relay origin it came in on,
	// whatever server it is for.
	req, err := http.NewRequest(http.MethodPost, relayB+edgeproto.PathUniversalMCP, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.id+".not-a-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	m := h.relay().Metrics()
	if n := m.ConnectorRequests[relay.ConnectorKey{Origin: relayB, Protocol: edgeproto.Version}]; n != 1 {
		t.Fatalf("connector requests %v, want one through %s to a server on protocol %d", m.ConnectorRequests, relayB, edgeproto.Version)
	}
	if m.ServersByOrigin[relayA] != 1 || m.ServersByOrigin[relayB] != 1 {
		t.Fatalf("servers by origin %v, want one at %s and one at %s", m.ServersByOrigin, relayA, relayB)
	}
	if logins := h.node.svc.DeviceLogins(); logins[signinB] != 1 {
		t.Fatalf("device logins %v, want one at %s", logins, signinB)
	}

	// Recording began with the second pair. The edge recorded each server
	// at the relay origin it enrolled at; the device token signed in at
	// the first pair before recording as seen there, at its first dial
	// through that pair's relay host; the one signed in at the second
	// pair as issued there, though it also dialed through the first. The
	// connector request is not recorded: this server refuses connector
	// connections, so it was never written to the server.
	rep, err := edgestore.ReadRetiring(context.Background(), filepath.Join(h.edgeDir, "edge.db"), time.Now())
	if err != nil || len(rep.Origins) != 2 || rep.RecordingSince.IsZero() {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	for i, want := range []struct {
		relay, signin, server    string
		issued, seen, connectors int
	}{{relayA, signinA, a.id, 0, 1, 0}, {relayB, signinB, b.id, 1, 0, 0}} {
		r := rep.Origins[i]
		if r.RelayOrigin != want.relay || r.SigninOrigin != want.signin || len(r.Servers) != 1 || r.Servers[0].ID != want.server ||
			r.Servers[0].AgentVersion != version.Version || r.Servers[0].OwnerLogin != alice.Login ||
			r.DeviceTokens.Total != want.issued || r.DeviceTokensSeen.Total != want.seen || r.ConnectorServers.Total != want.connectors {
			t.Errorf("report of %s: %+v, want server %s, %d device tokens issued, %d seen, %d servers with connector requests",
				want.relay, r, want.server, want.issued, want.seen, want.connectors)
		}
	}
}

// dialVia dials s as c through via and checks that the grant the
// server accepted named enrolled, the relay origin s enrolled at.
func dialVia(t *testing.T, h *harness, c *client, s *serverNode, via, enrolled string) {
	t.Helper()
	from := h.proxy.mark()
	sc, err := h.dial(c, cli.Config{ServerID: s.id, EdgeURL: via})
	if err != nil {
		t.Fatalf("%s to server %s through %s: %v", c.user.Login, s.id, via, err)
	}
	_ = sc.Close()
	e := h.proxy.await(t, "the open to "+s.id, from, func(e logEntry) bool {
		_, ok := e.msg.(edgeproto.Open)
		return ok && e.fromEdge && e.serverID == s.id
	})
	open := e.msg.(edgeproto.Open)
	scope := edgeproto.GrantScope{Issuer: enrolled, ServerID: s.id, ConnID: open.ConnID, Kind: open.Kind}
	if _, err := edgeproto.VerifyGrant(h.edgeKey().Public().(ed25519.PublicKey), open.Grant, scope, time.Now()); err != nil {
		t.Fatalf("grant through %s: %v", via, err)
	}
}

// readyTo is the ready the edge sent s after from.
func (h *harness) readyTo(t *testing.T, s *serverNode, from int) edgeproto.Ready {
	t.Helper()
	e := h.proxy.await(t, "ready to "+s.id, from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && e.fromEdge && r.ServerID == s.id
	})
	return e.msg.(edgeproto.Ready)
}

// TestConnectorRequestRecordedAtItsRelayOrigin sends, on a two-pair edge
// that records origins, a connector request through the second pair to a
// server enrolled at the first that accepts it, and one the server
// refuses: the store records the first at the second pair's relay origin
// only, and not the second.
func TestConnectorRequestRecordedAtItsRelayOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stopEdge()
	h.pairB, h.recordOrigins = true, true
	h.startEdge()
	relayA, relayB := h.relayURL, h.relayB
	accepts := h.newMCPServer(edgeproto.PolicyAccount, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	refuses := h.newServer(edgeproto.PolicyAccount)
	al := h.login(alice)[0]
	h.claimServer(al, accepts)
	h.claimServer(al, refuses)
	for _, s := range []*serverNode{refuses, accepts} {
		resp, err := http.Post(relayB+edgeproto.MCPPath(s.id), "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if s == accepts && resp.StatusCode != http.StatusOK {
			t.Fatalf("connector request to the accepting server: %s", resp.Status)
		}
	}
	path := filepath.Join(h.edgeDir, "edge.db")
	counts := func() (map[string]int, error) {
		rep, err := edgestore.ReadRetiring(context.Background(), path, time.Now())
		if err != nil {
			return nil, err
		}
		out := map[string]int{}
		for _, r := range rep.Origins {
			out[r.RelayOrigin] = r.ConnectorServers.Total
		}
		return out, nil
	}
	eventually(t, "the accepted request recorded at "+relayB, func() error {
		got, err := counts()
		if err == nil && got[relayB] == 0 {
			err = fmt.Errorf("servers with connector requests by relay origin: %v", got)
		}
		return err
	})
	time.Sleep(200 * time.Millisecond)
	got, err := counts()
	if err != nil || got[relayA] != 0 || got[relayB] != 1 {
		t.Fatalf("servers with connector requests by relay origin: %v, %v; want %s 1 and %s 0", got, err, relayB, relayA)
	}
}
