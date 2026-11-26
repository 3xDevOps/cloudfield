package localgw

import (
	"encoding/json"
	"io"
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
}

func newNoticeEdge(t *testing.T) *noticeEdge {
	t.Helper()
	e := &noticeEdge{}
	pub := make([]byte, 32)
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(e.Close)
	return e
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
	edge.set(func(e *noticeEdge) { e.retiring, e.delay = "https://new.example.test", noticeTimeout+500*time.Millisecond })
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

// A retired refusal carries its replacement in the error data, and
// edge.logout says when it deleted a token the edge did not revoke.
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
	if want := `{"edge":"` + edge.URL + `","unrevoked":{"device_id":"dev-1","replacement":"https://new.example.test"}}` + "\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("edge.logout = %d: %s, want %s", rec.Code, rec.Body, want)
	}
}
