package relay

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// endedAs fails unless the server's side of a relayed connection ended with
// refusal.
func endedAs(t *testing.T, server net.Conn, refusal edgeproto.Refusal) {
	t.Helper()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), string(refusal)) {
		t.Fatalf("the connection read %v; want it ended as %q", err, refusal)
	}
}

// stores waits until the edge has stored n directories.
func (e *env) stores(t *testing.T, n int) {
	t.Helper()
	eventually(t, "a directory stored", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return e.dir.dirWrites >= n
	})
}

// A member the directory does not name yet stands in for "a later
// directory": its store begins only once the store before it has ended the
// connections it ends.
var later = edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
	{Kind: edgeproto.EntryMember, Provider: edgeproto.ProviderGitHub, Subject: "later", Role: "viewer"},
}}

// The owner may connect as soon as the claim returns. A directory the
// server pushed before the claim does not name them, and storing it once
// the claim is recorded does not end their connection.
func TestOwnerOutlivesADirectoryPushedBeforeTheClaim(t *testing.T) {
	e := newEnv(t)
	e.r.directoryInterval = 50 * time.Millisecond
	a := enroll(t, e, newSigner(t))
	a.attach = true
	go a.run()
	acct := account("9")
	_, token := e.addDevice(t, acct, "dev-9")
	send(t, a.ws, edgeproto.Directory{})

	e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)
	open := next[edgeproto.Open](t, a)
	// The store of the older directory waits until the owner is connected.
	storing, store := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(store) })
	e.dir.mu.Lock()
	e.dir.replaceHook = func() {
		e.dir.mu.Lock()
		first := e.dir.dirWrites == 0
		e.dir.mu.Unlock()
		if first {
			close(storing)
			<-store
		}
	}
	e.dir.mu.Unlock()
	send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
	select {
	case <-storing:
	case <-time.After(5 * time.Second):
		t.Fatal("the directory pushed before the claim was not stored once the claim was recorded")
	}
	client, server := e.connect(t, a, token)
	store <- struct{}{}

	send(t, a.ws, later)
	e.stores(t, 2)
	roundTrip(t, client, server, "the owner's connection after the older directory was stored")
}

// A directory read before the claim can also reach the edge after the
// claim's report. It does not name the owner either, and does not end
// their connection.
func TestOwnerOutlivesADirectoryThatDoesNotNameThem(t *testing.T) {
	e := newEnv(t)
	e.r.directoryInterval = 50 * time.Millisecond
	a := claimedAgent(t, e)
	_, token := e.addDevice(t, account("owner"), "dev-owner")
	client, server := e.connect(t, a, token)

	send(t, a.ws, edgeproto.Directory{})
	e.stores(t, 1)
	send(t, a.ws, later)
	e.stores(t, 2)
	roundTrip(t, client, server, "the owner's connection after a directory that does not name them")
}

// `aether-edge servers remove` forgets a server without telling the relay.
// A member connected before it is cut off when the server next enrolls,
// before it can be claimed again.
func TestMemberOfARemovedServerIsCutWhenItEnrollsAgain(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := claimedAgentWith(t, e, signer)
	bob := account("2")
	e.dir.addMember(a.id, bob)
	_, bobToken := e.addDevice(t, bob, "bob-laptop")
	_, bobServer := e.connect(t, a, bobToken)

	e.dir.mu.Lock()
	delete(e.dir.owners, a.id)
	delete(e.dir.ownerOf, a.id)
	delete(e.dir.dirs, a.id)
	e.dir.mu.Unlock()
	again := enroll(t, e, signer)
	if again.ready.State != edgeproto.StateUnclaimed {
		t.Fatalf("the removed server enrolled as %s", again.ready.State)
	}
	endedAs(t, bobServer, edgeproto.RefusalUnknownServer)

	again.attach = true
	go again.run()
	alice := account("1")
	_, aliceToken := e.addDevice(t, alice, "alice-laptop")
	e.connectPath(t, again, edgeproto.ClaimConnectPath(again.id), aliceToken)
	open := next[edgeproto.Open](t, again)
	send(t, again.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(alice)})
	eventually(t, "the second claim recorded", func() bool { return e.r.Online(again.id) })
	client, server := e.connect(t, again, aliceToken)
	roundTrip(t, client, server, "the new owner's connection")
}

// A claim connection is admitted by the server's claim code. The pass at
// an unclaimed enrollment, which ends ssh connections to a server the edge
// does not know, leaves it open.
func TestClaimConnectionOutlivesTheServerEnrollingAgain(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := enroll(t, e, signer)
	a.attach = true
	go a.run()
	_, token := e.addDevice(t, account("7"), "dev-7")
	client, server := e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)

	enroll(t, e, signer)
	roundTrip(t, client, server, "the claim connection after its server enrolled again")
}

// When a server reports that its owner changed or that it has none, the
// former owner's connection ends, unless the directory names them.
func TestFormerOwnerIsCutUnlessTheDirectoryNamesThem(t *testing.T) {
	heir := edgeproto.Principal{Type: edgeproto.PrincipalAccount, Provider: edgeproto.ProviderGitHub, Subject: "9"}
	for _, report := range []string{"transfer", "ownerless"} {
		for _, named := range []bool{false, true} {
			name := report + ", owner not in the directory"
			if named {
				name = report + ", owner in the directory"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				a := claimedAgent(t, e)
				owner := account("owner")
				if named {
					e.dir.addMember(a.id, owner)
				}
				_, token := e.addDevice(t, owner, "dev-owner")
				client, server := e.connect(t, a, token)

				if report == "ownerless" {
					send(t, a.ws, edgeproto.Ownerless{})
					eventually(t, "the owner dropped", func() bool { _, ok := e.dir.owner(a.id); return !ok })
				} else {
					id := edgeproto.NewConnID()
					send(t, a.ws, edgeproto.OwnerTransferred{ID: id, Owner: heir})
					if got := next[edgeproto.OwnerTransferResult](t, a); got != (edgeproto.OwnerTransferResult{ID: id, Owner: heir}) {
						t.Fatalf("transfer answered %+v, want it recorded", got)
					}
				}
				// The answer to a refused transfer follows the end of the
				// report's handling, the pass over the connections included.
				e.dir.mu.Lock()
				e.dir.recordErr = edgeproto.Refusal("refused")
				e.dir.mu.Unlock()
				send(t, a.ws, edgeproto.OwnerTransferred{ID: edgeproto.NewConnID(), Owner: heir})
				if got := next[edgeproto.OwnerTransferResult](t, a); got.Error == "" {
					t.Fatalf("the second transfer answered %+v, want it refused", got)
				}
				if named {
					roundTrip(t, client, server, "the former owner's connection, a member's still")
					return
				}
				endedAs(t, server, edgeproto.RefusalNotMember)
			})
		}
	}
}

// A connection whose access the edge cannot check ends, and the pass goes
// on to the others.
func TestConnectionTheEdgeCannotCheckIsEnded(t *testing.T) {
	e := newEnv(t)
	e.r.directoryInterval = 50 * time.Millisecond
	a := claimedAgent(t, e)
	alice, bob := account("1"), account("2")
	e.dir.addMember(a.id, alice)
	e.dir.addMember(a.id, bob)
	_, aliceToken := e.addDevice(t, alice, "alice-laptop")
	_, bobToken := e.addDevice(t, bob, "bob-laptop")
	_, bobServer := e.connect(t, a, bobToken)
	aliceClient, aliceServer := e.connect(t, a, aliceToken)

	e.dir.mu.Lock()
	e.dir.admittedErr = map[edgeproto.Principal]error{edgeproto.AccountPrincipal(bob): errors.New("store unavailable")}
	e.dir.mu.Unlock()
	both := []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: alice.Provider, Subject: alice.Subject, Role: "admin"},
		{Kind: edgeproto.EntryMember, Provider: bob.Provider, Subject: bob.Subject, Role: "admin"},
	}
	send(t, a.ws, edgeproto.Directory{Entries: both})
	_ = bobServer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := bobServer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("the connection the edge could not check read %v; want it closed", err)
	}
	send(t, a.ws, edgeproto.Directory{Entries: append(both, later.Entries...)})
	e.stores(t, 2)
	roundTrip(t, aliceClient, aliceServer, "a checked member's connection")
}

// A connect the edge admitted before a directory dropped its account
// registers after that directory's pass. It is checked again before the
// server hears of it.
func TestConnectAdmittedBeforeItsAccountWasDroppedIsRefused(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	bob := account("2")
	e.dir.addMember(a.id, bob)
	_, token := e.addDevice(t, bob, "bob-laptop")
	e.dir.mu.Lock()
	e.dir.admitHook = func() {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		// The next Authenticate is the one after the connection registered.
		e.dir.authHook = func() {
			e.dir.mu.Lock()
			defer e.dir.mu.Unlock()
			e.dir.dirs[a.id] = nil
		}
	}
	e.dir.mu.Unlock()
	ws, resp, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.ConnectPath(a.id), &websocket.DialOptions{HTTPHeader: bearerHeader(token)})
	if ws != nil {
		_ = ws.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a connect whose account was dropped while it opened: %v, %+v; want it refused as not a member", err, resp)
	}
}
