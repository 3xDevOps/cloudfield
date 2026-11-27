package localgw

import (
	"context"
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
	// captured, when set, is sent to once the handler has read the
	// notice it answers with; it then waits for release.
	captured, release chan struct{}
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
		captured, release := e.captured, e.release
		e.captured, e.release = nil, nil
		e.mu.Unlock()
		if captured != nil {
			captured <- struct{}{}
			<-release
		}
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

// statusOf is the raw edge.status entry of origin.
func statusOf(t *testing.T, g *Gateway, origin string) map[string]json.RawMessage {
	t.Helper()
	rec := do(g, http.MethodPost, "/local/v1/edge.status", "{}", true)
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

func retiringOf(t *testing.T, g *Gateway, origin string) string {
	t.Helper()
	return string(statusOf(t, g, origin)["retiring"])
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func noticeGateway(t *testing.T) (*Gateway, *clock) {
	t.Helper()
	g := edgeGateway(t)
	c := &clock{now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	g.local.notices.now = c.get
	return g, c
}

// Every state edge.status reports, exact, and nothing for an edge with
// no notice or one whose values are not a canonical origin and a real
// date, printable and short.
func TestEdgeStatusNotice(t *testing.T) {
	const newEdge = "https://new.example.test"
	for _, tc := range []struct {
		name    string
		setup   func(e *noticeEdge)
		known   bool
		want    string
		wantRaw bool
	}{
		{name: "no notice", setup: func(*noticeEdge) {}},
		{name: "retiring", setup: func(e *noticeEdge) { e.retiring = newEdge },
			want: `{"state":"retiring","replacement":"https://new.example.test","known":false}`},
		{name: "retiring with a date", setup: func(e *noticeEdge) { e.retiring, e.date = newEdge, "2027-03-01" },
			want: `{"state":"retiring","replacement":"https://new.example.test","date":"2027-03-01","known":false}`},
		{name: "retiring to a known address", setup: func(e *noticeEdge) { e.retiring = newEdge }, known: true,
			want: `{"state":"retiring","replacement":"https://new.example.test","known":true}`},
		{name: "retired", setup: func(e *noticeEdge) {
			e.status, e.body = http.StatusGone, `{"error":"retired","retired":{"replacement":"https://new.example.test"}}`
		}, want: `{"state":"retired","replacement":"https://new.example.test","known":false}`},
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
			g, _ := noticeGateway(t)
			edge := newNoticeEdge(t)
			edge.set(tc.setup)
			if tc.known {
				signInTo(t, edge.URL, newEdge)
			} else {
				signInTo(t, edge.URL)
			}
			if got := retiringOf(t, g, edge.URL); got != tc.want {
				t.Fatalf("retiring = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEdgeStatusNoticeCache(t *testing.T) {
	g, clk := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	const first = `{"state":"retiring","replacement":"https://new.example.test","known":false}`
	edge.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	if got := retiringOf(t, g, edge.URL); got != first {
		t.Fatalf("retiring = %s", got)
	}

	clk.add(noticeRecheck - time.Second)
	edge.set(func(e *noticeEdge) { e.retiring = "https://other.example.test" })
	if got := retiringOf(t, g, edge.URL); got != first || edge.askedCount() != 1 {
		t.Fatalf("within the recheck the edge was asked %d times and the notice is %s", edge.askedCount(), got)
	}

	clk.add(time.Second)
	if got := retiringOf(t, g, edge.URL); got != `{"state":"retiring","replacement":"https://other.example.test","known":false}` {
		t.Fatalf("a changed replacement shows as %s", got)
	}

	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.status, e.body = http.StatusBadGateway, "down" })
	if got := retiringOf(t, g, edge.URL); !strings.Contains(got, "other.example.test") {
		t.Fatalf("a failed ask dropped the notice: %s", got)
	}
	clk.add(noticeMaxAge - noticeRecheck + time.Second)
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("a notice older than %v shows: %s", noticeMaxAge, got)
	}

	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.status, e.retiring = 0, "https://new.example.test" })
	if got := retiringOf(t, g, edge.URL); got != first {
		t.Fatalf("retiring = %s", got)
	}
	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.retiring = "" })
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("an empty answer kept %s", got)
	}

	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	if got := retiringOf(t, g, edge.URL); got != first {
		t.Fatalf("retiring = %s", got)
	}
	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.status, e.body = http.StatusGone, `{"error":"gone"}` })
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("a 410 without a replacement kept %s", got)
	}

	clk.add(noticeRecheck)
	edge.set(func(e *noticeEdge) { e.status = 0 })
	retiringOf(t, g, edge.URL)
	if rec := do(g, http.MethodPost, "/local/v1/edge.logout", `{"edge":"`+edge.URL+`"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("edge.logout = %d: %s", rec.Code, rec.Body)
	}
	if _, ok := g.local.notices.by[edge.URL]; ok {
		t.Fatal("logout kept the notice")
	}
}

// A slow edge holds edge.status for noticeTimeout at most, and is not
// asked again until the recheck.
func TestEdgeStatusNoticeSlowEdge(t *testing.T) {
	g, _ := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	edge.set(func(e *noticeEdge) {
		e.retiring, e.delay = "https://new.example.test", noticeTimeout+500*time.Millisecond
	})
	start := time.Now()
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("retiring = %s", got)
	}
	if took := time.Since(start); took > noticeTimeout+time.Second {
		t.Fatalf("edge.status took %v", took)
	}
	start = time.Now()
	retiringOf(t, g, edge.URL)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("edge.status asked the slow edge again, took %v", took)
	}
}

// Two edge.status calls during one slow ask share it and both see the
// notice; the edge is asked once.
func TestEdgeStatusNoticeSharedAsk(t *testing.T) {
	g, _ := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	edge.set(func(e *noticeEdge) { e.retiring, e.delay = "https://new.example.test", 300*time.Millisecond })
	got := make([]string, 2)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() { got[i] = retiringOf(t, g, edge.URL) })
	}
	wg.Wait()
	for _, r := range got {
		if !strings.Contains(r, "new.example.test") {
			t.Fatalf("edge.status answers %q, want the notice in both", got)
		}
	}
	if n := edge.askedCount(); n != 1 {
		t.Fatalf("the edge was asked %d times", n)
	}
}

// A request that ends before the edge answers does not end the ask: the
// next edge.status shows the answer.
func TestEdgeStatusNoticeOutlivesItsRequest(t *testing.T) {
	g, _ := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	edge.set(func(e *noticeEdge) { e.retiring, e.delay = "https://new.example.test", 200*time.Millisecond })
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := g.local.notices.lookup(g.ctx, ctx, dir, []string{edge.URL}, g.local.notices.generation()); len(got) != 0 {
		t.Fatalf("an ended request got %v", got)
	}
	time.Sleep(400 * time.Millisecond)
	if got := retiringOf(t, g, edge.URL); !strings.Contains(got, "new.example.test") || edge.askedCount() != 1 {
		t.Fatalf("after the ended request: %s, asked %d times", got, edge.askedCount())
	}
}

// An answer the edge gave before a logout and a new sign-in is dropped,
// and an edge no longer signed in is forgotten.
func TestEdgeStatusNoticeAfterSignOut(t *testing.T) {
	g, _ := noticeGateway(t)
	edge, other := newNoticeEdge(t), newNoticeEdge(t)
	signInTo(t, edge.URL, other.URL)
	captured, release := make(chan struct{}), make(chan struct{})
	edge.set(func(e *noticeEdge) { e.retiring, e.captured, e.release = "https://new.example.test", captured, release })
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.local.notices.lookup(g.ctx, ctx, dir, []string{edge.URL, other.URL}, g.local.notices.generation())
	<-captured
	g.local.notices.mu.Lock()
	oldAsk := g.local.notices.by[edge.URL].asking
	g.local.notices.mu.Unlock()
	g.local.notices.forget(edge.URL)
	g.local.notices.forget(edge.URL)
	edge.set(func(e *noticeEdge) { e.retiring = "" })
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("retiring = %s", got)
	}
	close(release)
	<-oldAsk
	if got := retiringOf(t, g, edge.URL); got != "" {
		t.Fatalf("the answer from before the sign-out shows: %s", got)
	}
	signInTo(t, edge.URL)
	retiringOf(t, g, edge.URL)
	if _, ok := g.local.notices.by[other.URL]; ok {
		t.Fatal("an edge no longer signed in is still cached")
	}
}

// A status call whose signed-in rows were read before a logout neither
// brings the forgotten edge back nor prunes, and asks nothing.
func TestEdgeStatusNoticeFromAnObsoleteSnapshot(t *testing.T) {
	g, _ := noticeGateway(t)
	edge, other := newNoticeEdge(t), newNoticeEdge(t)
	signInTo(t, edge.URL, other.URL)
	edge.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	retiringOf(t, g, other.URL)
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	gen := g.local.notices.generation()
	g.local.notices.forget(edge.URL)
	if got := g.local.notices.lookup(g.ctx, context.Background(), dir, []string{edge.URL}, gen); len(got) != 0 {
		t.Fatalf("an obsolete snapshot got %v", got)
	}
	if _, ok := g.local.notices.by[edge.URL]; ok {
		t.Fatal("an obsolete snapshot brought the forgotten edge back")
	}
	if _, ok := g.local.notices.by[other.URL]; !ok {
		t.Fatal("an obsolete snapshot pruned an edge")
	}
	if n := edge.askedCount(); n != 1 {
		t.Fatalf("the edge was asked %d times", n)
	}
}

// A saved config that cannot be read leaves the notice, as not known.
func TestEdgeStatusNoticeWithAnUnreadableConfig(t *testing.T) {
	g, _ := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL, "https://new.example.test")
	edge.set(func(e *noticeEdge) { e.retiring = "https://new.example.test" })
	path, err := cli.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := retiringOf(t, g, edge.URL); got != `{"state":"retiring","replacement":"https://new.example.test","known":false}` {
		t.Fatalf("retiring = %s", got)
	}
}

// Every ask closes its connection.
func TestEdgeStatusNoticeClosesConnections(t *testing.T) {
	g, clk := noticeGateway(t)
	edge := newNoticeEdge(t)
	signInTo(t, edge.URL)
	for range 4 {
		retiringOf(t, g, edge.URL)
		clk.add(noticeRecheck)
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
	g, _ := noticeGateway(t)
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
