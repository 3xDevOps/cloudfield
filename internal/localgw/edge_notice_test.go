package localgw

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// noticeEdge answers GET /v1/edge with retiring and date, or with status
// and body when status is set, after delay.
type noticeEdge struct {
	*httptest.Server
	mu               sync.Mutex
	retiring, date   string
	status           int
	body             string
	delay            time.Duration
	asked, loggedOut int
	open             map[net.Conn]bool
	opened           int
}

func newNoticeEdge(t *testing.T) *noticeEdge {
	t.Helper()
	e := &noticeEdge{open: map[net.Conn]bool{}}
	pub := make([]byte, 32)
	e.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		status, body, delay := e.status, e.body, e.delay
		info := edgeproto.EdgeInfo{SigninOrigin: e.URL, Key: pub, Fingerprint: edgeproto.EdgeKeyFingerprint(pub),
			Version: edgeproto.Version, MinVersion: edgeproto.MinVersion, Retiring: e.retiring, RetiringDate: e.date}
		if r.URL.Path == edgeproto.PathEdgeInfo {
			e.asked++
		} else {
			e.loggedOut++
		}
		e.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set(edgeproto.HeaderVersion, "1")
		switch {
		case r.URL.Path == edgeproto.PathLogout:
			w.WriteHeader(http.StatusNoContent)
		case status != 0:
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			_ = json.NewEncoder(w).Encode(info)
		}
	}))
	e.Config.ConnState = func(c net.Conn, state http.ConnState) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch state {
		case http.StateNew:
			e.opened++
			e.open[c] = true
		case http.StateClosed, http.StateHijacked:
			delete(e.open, c)
		}
	}
	e.Start()
	t.Cleanup(e.Close)
	return e
}

func (e *noticeEdge) conns() (open, opened int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.open), e.opened
}

func (e *noticeEdge) set(f func(e *noticeEdge)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

func (e *noticeEdge) askedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asked
}

// signInTo writes a device token for each of origins.
func signInTo(t *testing.T, origins ...string) {
	t.Helper()
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	edges := map[string]any{}
	for _, o := range origins {
		edges[o] = map[string]any{"token": edgeproto.NewToken(), "signin_origin": o, "device": map[string]any{"id": "dev-1", "label": "laptop"}}
	}
	raw, err := json.Marshal(map[string]any{"edges": edges})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, edgeclient.TokensFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// statusOf is the raw edge.status entry of origin, asked with params.
func statusOf(t *testing.T, g *Gateway, params, origin string) map[string]json.RawMessage {
	t.Helper()
	rec := do(g, http.MethodPost, "/local/v1/edge.status", params, true)
	var st struct {
		Edges []map[string]json.RawMessage `json:"edges"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("edge.status = %d: %s", rec.Code, rec.Body)
	}
	for _, e := range st.Edges {
		if string(e["edge"]) == `"`+origin+`"` {
			return e
		}
	}
	t.Fatalf("edge.status lacks %s: %s", origin, rec.Body)
	return nil
}

// retiringOf is the notice edge.status reports for origin when asked for
// notices.
func retiringOf(t *testing.T, g *Gateway, origin string) string {
	t.Helper()
	return string(statusOf(t, g, `{"notices":true}`, origin)["retiring"])
}

// Every state edge.status reports, exact, and nothing for an edge with
// no notice or one whose values are not a canonical origin and a real
// date, printable and short.
func TestEdgeStatusNotice(t *testing.T) {
	const newEdge = "https://new.example.test"
	for _, tc := range []struct {
		name    string
		setup   func(e *noticeEdge)
		want    string
		wantRaw bool
	}{
		{name: "no notice", setup: func(*noticeEdge) {}},
		{name: "retiring", setup: func(e *noticeEdge) { e.retiring = newEdge },
			want: `{"state":"retiring","replacement":"https://new.example.test"}`},
		{name: "retiring with a date", setup: func(e *noticeEdge) { e.retiring, e.date = newEdge, "2027-03-01" },
			want: `{"state":"retiring","replacement":"https://new.example.test","date":"2027-03-01"}`},
		{name: "retired", setup: func(e *noticeEdge) {
			e.status, e.body = http.StatusGone, `{"error":"retired","retired":{"replacement":"https://new.example.test"}}`
		}, want: `{"state":"retired","replacement":"https://new.example.test"}`},
		{name: "gone without retired", setup: func(e *noticeEdge) { e.status, e.body = http.StatusGone, `{"error":"gone"}` }},
		{name: "the replacement is the same edge", setup: func(e *noticeEdge) { e.retiring = e.URL }},
		{name: "retired in favour of the same edge", setup: func(e *noticeEdge) {
			e.status, e.body = http.StatusGone, `{"error":"retired","retired":{"replacement":"`+e.URL+`"}}`
		}},
		{name: "not an origin", setup: func(e *noticeEdge) { e.retiring = "new.example.test" }},
		{name: "a path", setup: func(e *noticeEdge) { e.retiring = newEdge + "/x" }},
		{name: "control characters", setup: func(e *noticeEdge) { e.retiring = newEdge + "\x1b[2J" }},
		{name: "very long", setup: func(e *noticeEdge) { e.retiring = "https://" + strings.Repeat("a.", 140) + "example.test" }},
		{name: "a date that is not real", setup: func(e *noticeEdge) { e.retiring, e.date = newEdge, "2027-02-30" }},
		{name: "retired names a control character", setup: func(e *noticeEdge) {
			e.status, e.body = http.StatusGone, `{"error":"retired","retired":{"replacement":"https://new.example.test\u001b"}}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := edgeGateway(t)
			edge := newNoticeEdge(t)
			edge.set(tc.setup)
			signInTo(t, edge.URL)
			if got := retiringOf(t, g, edge.URL); got != tc.want {
				t.Fatalf("retiring = %s, want %s", got, tc.want)
			}
		})
	}
}

// Without notices in its params edge.status asks no edge; with them it
// asks again on every call and keeps nothing.
func TestEdgeStatusNoticeOnlyOnRequest(t *testing.T) {
	g := edgeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	edge.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	for _, params := range []string{"{}", `{"notices":false}`} {
		if got := statusOf(t, g, params, edge.URL)["retiring"]; got != nil || edge.askedCount() != 0 {
			t.Fatalf("edge.status %s: retiring %s, the edge asked %d times", params, got, edge.askedCount())
		}
	}
	if got := retiringOf(t, g, edge.URL); got != `{"state":"retiring","replacement":"https://new.example.test"}` {
		t.Fatalf("retiring = %s", got)
	}
	edge.set(func(e *noticeEdge) { e.retiring = "" })
	if got := retiringOf(t, g, edge.URL); got != "" || edge.askedCount() != 2 {
		t.Fatalf("second call: retiring %s, the edge asked %d times", got, edge.askedCount())
	}
}

// Slow edges are asked at once and hold edge.status for noticeTimeout at
// most in all; a fast one still reports its notice.
func TestEdgeStatusNoticeSlowEdges(t *testing.T) {
	g := edgeGateway(t)
	slow1, slow2, fast := newNoticeEdge(t), newNoticeEdge(t), newNoticeEdge(t)
	signInTo(t, slow1.URL, slow2.URL, fast.URL)
	for _, e := range []*noticeEdge{slow1, slow2, fast} {
		e.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	}
	for _, e := range []*noticeEdge{slow1, slow2} {
		e.set(func(e *noticeEdge) { e.delay = noticeTimeout + 500*time.Millisecond })
	}
	start := time.Now()
	rec := do(g, http.MethodPost, "/local/v1/edge.status", `{"notices":true}`, true)
	if took := time.Since(start); took > noticeTimeout+time.Second {
		t.Fatalf("edge.status took %v", took)
	}
	var st struct {
		Edges []signedInEdge `json:"edges"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("edge.status = %d: %s", rec.Code, rec.Body)
	}
	for _, e := range st.Edges {
		if (e.Retiring != nil) != (e.Edge == fast.URL) {
			t.Fatalf("edge %s retiring %+v", e.Edge, e.Retiring)
		}
	}
}

// The asks share one connection across calls, and the gateway closes it
// when it closes.
func TestEdgeStatusNoticeReusesItsConnection(t *testing.T) {
	g := edgeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	for range 4 {
		retiringOf(t, g, edge.URL)
	}
	if _, opened := edge.conns(); opened != 1 || edge.askedCount() != 4 {
		t.Fatalf("4 asks opened %d connections, asked %d times", opened, edge.askedCount())
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		open, _ := edge.conns()
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections to the edge are still open", open)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A retired refusal carries its replacement in the error data, and
// edge.logout there deletes the token.
func TestEdgeLogoutAtARetiredAddress(t *testing.T) {
	g := edgeGateway(t)
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edgeproto.HeaderVersion, "1")
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, `{"error":"https://old.example.test is retired","retired":{"replacement":"https://new.example.test"}}`)
	}))
	t.Cleanup(edge.Close)
	signInTo(t, edge.URL)
	rec := do(g, http.MethodPost, "/local/v1/edge.servers", `{"edge":"`+edge.URL+`"}`, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"retired":{"replacement":"https://new.example.test"}`) ||
		!strings.Contains(rec.Body.String(), `"refusal":"https://old.example.test is retired"`) {
		t.Fatalf("edge.servers = %d: %s", rec.Code, rec.Body)
	}
	rec = do(g, http.MethodPost, "/local/v1/edge.logout", `{"edge":"`+edge.URL+`"}`, true)
	if want := `{"edge":"` + edge.URL + `"}` + "\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("edge.logout = %d: %s, want %s", rec.Code, rec.Body, want)
	}
	if dir, err := cli.Dir(); err != nil {
		t.Fatal(err)
	} else if signedIn, err := edgeclient.SignedIn(dir); err != nil || len(signedIn) != 0 {
		t.Fatalf("signed in to %v (%v) after logout at a retired address", signedIn, err)
	}
}
