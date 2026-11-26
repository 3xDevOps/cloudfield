package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// TestEnrollmentPassesOrigin checks that the directory gets the relay
// origin and the build of every enrollment, claimed or not.
func TestEnrollmentPassesOrigin(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	c := claimedAgent(t, e)
	e.dir.mu.Lock()
	got := slices.Clone(e.dir.enrollments)
	e.dir.mu.Unlock()
	if want := []string{a.id + " " + e.origin + " test", c.id + " " + e.origin + " test"}; !slices.Equal(got, want) {
		t.Fatalf("enrollments = %q, want %q", got, want)
	}
}

// TestConnectorUseIsRecordedHourly sends connector requests to a claimed
// server: the directory records the first, then none until an hour has
// passed, and nothing for an unclaimed or an offline server.
func TestConnectorUseIsRecordedHourly(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	serveData(t, a, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	unclaimed := enroll(t, e, newSigner(t))
	post := func(id string) int { return postMCP(t, e, id) }
	uses := func() []string { return connectorUses(e) }
	one := []string{a.id + " " + e.origin}
	for range 3 {
		if status := post(a.id); status != http.StatusOK {
			t.Fatalf("connector request: %d", status)
		}
	}
	eventually(t, "the first request recorded", func() bool { return len(uses()) == 1 })
	if got := uses(); !slices.Equal(got, one) {
		t.Fatalf("after three requests: %q, want %q", got, one)
	}
	post(unclaimed.id)
	post("cccccccccccccccccccccccccc")
	if got := uses(); !slices.Equal(got, one) {
		t.Fatalf("after requests to an unclaimed and an offline server: %q, want %q", got, one)
	}
	e.r.mu.Lock()
	n := len(e.r.connectorUsed)
	e.r.connectorUsed[connectorUse{a.id, e.origin}] = time.Now().Add(-connectorUseInterval)
	e.r.mu.Unlock()
	if n != 1 {
		t.Fatalf("the guard holds %d entries, want 1", n)
	}
	post(a.id)
	eventually(t, "the request recorded again after an hour", func() bool { return slices.Equal(uses(), append(one, one...)) })
}

const okAnswer = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"

// postMCP sends a connector request for id and returns its status.
func postMCP(t *testing.T, e *env, id string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+edgeproto.MCPPath(id), strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func connectorUses(e *env) []string {
	e.dir.mu.Lock()
	defer e.dir.mu.Unlock()
	return slices.Clone(e.dir.connectorUses)
}

// TestRefusedConnectorRequestIsNotRecorded sends a connector request the
// edge itself refuses, to a claimed server whose protocol predates
// connector requests: the server never gets it, so nothing is recorded.
func TestRefusedConnectorRequestIsNotRecorded(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	id := edgeproto.ServerID(signer.PublicKey())
	e.dir.mu.Lock()
	e.dir.owners[id] = true
	e.dir.ownerOf[id] = edgeproto.AccountPrincipal(account("owner"))
	e.dir.mu.Unlock()
	a := enrollAt(t, e, signer, edgeproto.VersionHTTP-1)
	go a.run()
	if status := postMCP(t, e, id); status == http.StatusOK {
		t.Fatalf("request to a server before protocol %d: %d", edgeproto.VersionHTTP, status)
	}
	time.Sleep(50 * time.Millisecond)
	if got := connectorUses(e); len(got) != 0 {
		t.Fatalf("a request the edge refused was recorded: %q", got)
	}
}

// TestReclaimedServerIsRecordedAgain records a connector request, has the
// server removed and claimed again within the hour, and records its next
// request: the claim drops the earlier mark.
func TestReclaimedServerIsRecordedAgain(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := claimedAgentWith(t, e, signer)
	serveData(t, a, okAnswer)
	if status := postMCP(t, e, a.id); status != http.StatusOK {
		t.Fatalf("connector request: %d", status)
	}
	eventually(t, "the first request recorded", func() bool { return len(connectorUses(e)) == 1 })

	e.dir.mu.Lock()
	delete(e.dir.owners, a.id)
	delete(e.dir.ownerOf, a.id)
	e.dir.mu.Unlock()
	if err := a.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	b := enroll(t, e, signer)
	b.attach = true
	go b.run()
	acct := account("7")
	_, token := e.addDevice(t, acct, "dev-7")
	client, server := e.connectPath(t, b, edgeproto.ClaimConnectPath(b.id), token)
	open := next[edgeproto.Open](t, b)
	_ = client.Close()
	_ = server.Close()
	send(t, b.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
	eventually(t, "the claim recorded", func() bool { return e.r.Online(b.id) })
	serveData(t, b, okAnswer)
	if status := postMCP(t, e, b.id); status != http.StatusOK {
		t.Fatalf("connector request after the claim: %d", status)
	}
	eventually(t, "the request after the claim recorded", func() bool { return len(connectorUses(e)) == 2 })
}

// TestHeldConnectorRequestIsRecorded holds a connector request while its
// server reconnects: it is recorded when the server takes it, not before.
func TestHeldConnectorRequestIsRecorded(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := claimedAgentWith(t, e, signer)
	if err := a.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the server away", func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return e.r.away[a.id] != nil
	})
	done := make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+edgeproto.MCPPath(a.id), strings.NewReader("{}"))
		if err != nil {
			done <- 0
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	eventually(t, "the request held", func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return e.r.httpWaiting[a.id] > 0
	})
	e.dir.mu.Lock()
	early := len(e.dir.connectorUses)
	e.dir.mu.Unlock()
	if early != 0 {
		t.Fatalf("a held request was recorded before its server took it: %d", early)
	}
	b := enroll(t, e, signer)
	b.attach = true
	go b.run()
	serveData(t, b, okAnswer)
	if status := <-done; status != http.StatusOK {
		t.Fatalf("held request: %d", status)
	}
	eventually(t, "the held request recorded", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return slices.Equal(e.dir.connectorUses, []string{a.id + " " + e.origin})
	})
}

// recorder is a ConnectorRecorder whose writes wait for release and then
// fail with err.
type recorder struct {
	mu      sync.Mutex
	calls   []string
	ctx     context.Context
	release chan struct{}
	err     error
}

func (c *recorder) ConnectorUsed(ctx context.Context, serverID, origin string) error {
	c.mu.Lock()
	c.calls = append(c.calls, serverID+" "+origin)
	c.ctx = ctx
	release, err := c.release, c.err
	c.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (c *recorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

const (
	recID     = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	recOrigin = "https://relay.example.test"
)

// relayWith is a relay whose recorder is rec, holding a claimed
// registration for recID.
func relayWith(t *testing.T, rec ConnectorRecorder) *Relay {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), Config{Origin: recOrigin, EdgeKey: key, Directory: &fakeDir{},
		Egress: &fakeEgress{months: map[string]int64{}}, Connectors: rec})
	if err != nil {
		t.Fatal(err)
	}
	r.servers[recID] = &registration{id: recID, claimed: true, version: edgeproto.Version}
	return r
}

func (r *Relay) mark() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.connectorUsed[connectorUse{recID, recOrigin}]
	return at, ok
}

// TestConnectorUseWithoutRecorder keeps no record, in memory or written,
// on a relay built without a ConnectorRecorder.
func TestConnectorUseWithoutRecorder(t *testing.T) {
	r := relayWith(t, nil)
	reg := r.servers[recID]
	r.noteConnector(t.Context(), reg, recOrigin)
	if _, ok := r.mark(); ok {
		t.Fatal("a relay without a recorder keeps a mark")
	}
}

// TestConnectorUseWrite checks that noteConnector does not wait for the
// write, that the write runs on a context the client's hang-up does not
// cancel, that requests during it start no second write, that a failed
// write is tried again after connectorRetry and not before, and that
// Shutdown waits for a write under way.
func TestConnectorUseWrite(t *testing.T) {
	rec := &recorder{release: make(chan struct{}), err: errors.New("database is locked")}
	r := relayWith(t, rec)
	reg := r.servers[recID]
	client, hangUp := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		r.noteConnector(client, reg, recOrigin)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("noteConnector waited for the write")
	}
	eventually(t, "the write started", func() bool { return rec.count() == 1 })
	hangUp()
	rec.mu.Lock()
	worker := rec.ctx
	rec.mu.Unlock()
	if err := worker.Err(); err != nil {
		t.Fatalf("the client's hang-up reached the write: %v", err)
	}
	r.noteConnector(t.Context(), reg, recOrigin)
	if n := rec.count(); n != 1 {
		t.Fatalf("a request during the write started another: %d writes", n)
	}
	close(rec.release)
	eventually(t, "the failed write's mark moved to the retry time", func() bool {
		at, ok := r.mark()
		return ok && time.Until(at.Add(connectorUseInterval)) <= connectorRetry && time.Until(at.Add(connectorUseInterval)) > 0
	})
	rec.mu.Lock()
	rec.release, rec.err = nil, nil
	rec.mu.Unlock()
	r.noteConnector(t.Context(), reg, recOrigin)
	if n := rec.count(); n != 1 {
		t.Fatalf("a request within the retry time wrote: %d writes", n)
	}
	r.mu.Lock()
	r.connectorUsed[connectorUse{recID, recOrigin}] = time.Now().Add(-connectorUseInterval)
	r.mu.Unlock()
	r.noteConnector(t.Context(), reg, recOrigin)
	eventually(t, "the request after the retry time writes", func() bool { return rec.count() == 2 })
	eventually(t, "the successful write keeps its mark", func() bool {
		at, ok := r.mark()
		return ok && time.Since(at) < time.Minute
	})
	r.noteConnector(t.Context(), reg, recOrigin)

	block := make(chan struct{})
	rec.mu.Lock()
	rec.release = block
	rec.mu.Unlock()
	r.mu.Lock()
	r.connectorUsed[connectorUse{recID, recOrigin}] = time.Now().Add(-connectorUseInterval)
	r.mu.Unlock()
	r.noteConnector(t.Context(), reg, recOrigin)
	eventually(t, "a third write under way", func() bool { return rec.count() == 3 })
	r.mu.Lock()
	delete(r.servers, recID)
	r.mu.Unlock()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Shutdown(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned with a write under way: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(block)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.connectorUsed[connectorUse{recID, recOrigin}] = time.Now().Add(-connectorUseInterval)
	r.mu.Unlock()
	r.noteConnector(t.Context(), reg, recOrigin)
	time.Sleep(50 * time.Millisecond)
	if n := rec.count(); n != 3 {
		t.Fatalf("a closing relay started a write: %d writes", n)
	}
}
