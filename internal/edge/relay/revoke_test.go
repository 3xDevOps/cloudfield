package relay

import (
	"errors"
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

// checked returns a wait for the edge to ask once more whether a may
// connect, as a pass over the relayed connections does.
func (e *env) checked(t *testing.T, a edgeproto.Account) func() {
	t.Helper()
	calls := func() int {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return e.dir.admittedCalls[edgeproto.AccountPrincipal(a)]
	}
	before := calls()
	return func() {
		t.Helper()
		eventually(t, "the pass asking about a connection", func() bool { return calls() > before })
	}
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

// A claim connection is admitted by the server's claim code. A pass over
// the connections to its server, which the edge does not know yet, leaves
// it open.
func TestPassLeavesAClaimConnection(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	a.attach = true
	go a.run()
	_, token := e.addDevice(t, account("7"), "dev-7")
	client, server := e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)

	e.r.revoke(a.id)
	roundTrip(t, client, server, "the claim connection after a pass")
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
				passed := e.checked(t, owner)

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
				if named {
					// The report's pass runs beside the reader; one more,
					// finished here, shows what a pass does to a member.
					passed()
					e.r.revoke(a.id)
					roundTrip(t, client, server, "the former owner's connection, a member's still")
					return
				}
				endedAs(t, server, edgeproto.RefusalNotMember)
			})
		}
	}
}

// A connection whose access the edge cannot check ends, told why, and the
// pass goes on to the others.
func TestConnectionTheEdgeCannotCheckIsEnded(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	alice, bob, carol := account("1"), account("2"), account("3")
	var entries []edgeproto.DirectoryEntry
	clients := map[edgeproto.Account]net.Conn{}
	var aliceServer net.Conn
	for _, acct := range []edgeproto.Account{alice, bob, carol} {
		e.dir.addMember(a.id, acct)
		entries = append(entries, edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember, Provider: acct.Provider, Subject: acct.Subject, Role: "admin"})
		_, token := e.addDevice(t, acct, "laptop-"+acct.Subject)
		client, server := e.connect(t, a, token)
		clients[acct] = client
		if acct == alice {
			aliceServer = server
		}
	}
	passed := e.checked(t, alice)
	e.dir.mu.Lock()
	e.dir.admittedErr = map[edgeproto.Principal]error{
		edgeproto.AccountPrincipal(bob):   errors.New("store unavailable"),
		edgeproto.AccountPrincipal(carol): errors.New("store unavailable"),
	}
	e.dir.mu.Unlock()
	send(t, a.ws, edgeproto.Directory{Entries: append(entries, later.Entries...)})
	for _, acct := range []edgeproto.Account{bob, carol} {
		_ = clients[acct].SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := clients[acct].Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), "could not check this connection's access") {
			t.Fatalf("a connection the edge could not check read %v; want it closed with the reason", err)
		}
	}
	passed()
	roundTrip(t, clients[alice], aliceServer, "a checked member's connection")
	e.dir.mu.Lock()
	stores := e.dir.dirWrites
	e.dir.mu.Unlock()
	if stores != 1 {
		t.Fatalf("%d directories stored; want the two closures from one pass", stores)
	}
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

// Reports that arrive while a pass runs do not start a pass each: one
// more runs when it ends.
func TestPassesForOneServerDoNotPileUp(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	owner := account("owner")
	_, token := e.addDevice(t, owner, "dev-owner")
	e.connect(t, a, token)

	asking, answer := make(chan struct{}, 8), make(chan struct{})
	e.dir.mu.Lock()
	e.dir.admittedHook = func() {
		asking <- struct{}{}
		<-answer
	}
	e.dir.mu.Unlock()
	for range 5 {
		e.r.revokeSoon(a.id)
	}
	select {
	case <-asking:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass ran")
	}
	select {
	case <-asking:
		t.Fatal("a second pass over the server's connections began while the first was running")
	case <-time.After(100 * time.Millisecond):
	}
	close(answer)
	select {
	case <-asking:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass asked for while one was running never ran")
	}
	eventually(t, "the passes ending", func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return len(e.r.revoking) == 0
	})
	if n := len(asking); n != 0 {
		t.Fatalf("%d passes more than the one running and the one asked for meanwhile", n)
	}
}

// A connect whose second check gets no answer is refused, told why.
func TestConnectTheEdgeCannotCheckIsRefused(t *testing.T) {
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
			e.dir.admittedErr = map[edgeproto.Principal]error{edgeproto.AccountPrincipal(bob): errors.New("store unavailable")}
		}
	}
	e.dir.mu.Unlock()
	status, body := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "could not check this connection's access") {
		t.Fatalf("a connect the edge could not check: %d %q; want 503 and the reason", status, body)
	}
}
