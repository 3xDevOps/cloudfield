package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// TestEnrollmentPassesOrigin checks that the directory gets the relay
// origin and the build of every enrollment, claimed or not, and of a
// claim.
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
	post := func(id string) int {
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
	uses := func() []string {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return slices.Clone(e.dir.connectorUses)
	}
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

// recorder is a ConnectorRecorder whose writes wait for release and then
// fail with err.
type recorder struct {
	mu      sync.Mutex
	calls   []string
	release chan struct{}
	err     error
}

func (c *recorder) ConnectorUsed(ctx context.Context, serverID, origin string) error {
	c.mu.Lock()
	c.calls = append(c.calls, serverID+" "+origin)
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

// relayWith is a relay whose recorder is rec, holding a claimed
// registration for id, and a request for it on the relay origin.
func relayWith(t *testing.T, rec ConnectorRecorder, id string) (*Relay, *http.Request) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), Config{Origin: "https://relay.example.test", EdgeKey: key, Directory: &fakeDir{},
		Egress: &fakeEgress{months: map[string]int64{}}, Connectors: rec})
	if err != nil {
		t.Fatal(err)
	}
	r.servers[id] = &registration{id: id, claimed: true, version: edgeproto.Version}
	return r, httptest.NewRequest(http.MethodPost, "https://relay.example.test"+edgeproto.MCPPath(id), nil)
}

// TestConnectorUseWithoutRecorder keeps no record, in memory or written,
// on a relay built without a ConnectorRecorder, and still counts.
func TestConnectorUseWithoutRecorder(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	r, req := relayWith(t, nil, id)
	r.countConnector(req, id)
	r.mu.Lock()
	n := len(r.connectorUsed)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("the guard holds %d entries without a recorder", n)
	}
	if got := r.Metrics().ConnectorRequests[ConnectorKey{Origin: "https://relay.example.test", Protocol: edgeproto.Version}]; got != 1 {
		t.Fatalf("connector requests counted: %d, want 1", got)
	}
}

// TestConnectorUseWriteIsBackground checks that countConnector does not
// wait for the write, that a client hanging up does not cancel it, that
// requests during it start no second write, and that a failed write is
// tried again by the next request.
func TestConnectorUseWriteIsBackground(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	rec := &recorder{release: make(chan struct{}), err: errors.New("database is locked")}
	r, req := relayWith(t, rec, id)
	ctx, hangUp := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		r.countConnector(req.WithContext(ctx), id)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("countConnector waited for the write")
	}
	hangUp()
	eventually(t, "the write started", func() bool { return rec.count() == 1 })
	r.countConnector(req, id)
	if n := rec.count(); n != 1 {
		t.Fatalf("a request during the write started another: %d writes", n)
	}
	close(rec.release)
	eventually(t, "the failed write's reservation dropped", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, ok := r.connectorUsed[connectorUse{id, "https://relay.example.test"}]
		return !ok
	})
	rec.mu.Lock()
	rec.release, rec.err = nil, nil
	rec.mu.Unlock()
	r.countConnector(req, id)
	eventually(t, "the next request writes again", func() bool { return rec.count() == 2 })
	r.countConnector(req, id)
	time.Sleep(50 * time.Millisecond)
	if n := rec.count(); n != 2 {
		t.Fatalf("a request after a successful write wrote again: %d writes", n)
	}
}
