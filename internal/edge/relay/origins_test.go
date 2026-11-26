package relay

import (
	"io"
	"net/http"
	"slices"
	"strings"
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
	if got := uses(); !slices.Equal(got, append(one, one...)) {
		t.Fatalf("after an hour: %q, want the request recorded again", got)
	}
}
