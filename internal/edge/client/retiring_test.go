package edgeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

var retiringCases = []struct {
	value string
	want  Retiring
	ok    bool
}{
	{"", Retiring{}, false},
	{"https://edge2.example.test", Retiring{Replacement: "https://edge2.example.test"}, true},
	{"https://edge2.example.test; date=2027-03-01", Retiring{Replacement: "https://edge2.example.test", Date: "2027-03-01"}, true},
	{"https://edge2.example.test; date=2027-02-30", Retiring{}, false},
	{"https://edge2.example.test; date=tomorrow", Retiring{}, false},
	{"https://EDGE2.example.test", Retiring{}, false},
	{"https://edge2.example.test/login?code=x", Retiring{}, false},
	{"edge2.example.test", Retiring{}, false},
}

// TestRetiringHeader checks every value of Aether-Edge-Retiring an answer
// may carry, on an API answer and on a refused relayed connection, and
// that an answer from something in between changes nothing.
func TestRetiringHeader(t *testing.T) {
	var mu sync.Mutex
	value, fromEdge := "", true
	set := func(w http.ResponseWriter) {
		mu.Lock()
		defer mu.Unlock()
		if value != "" {
			w.Header().Set(edgeproto.HeaderRetiring, value)
		}
		if !fromEdge {
			w.WriteHeader(http.StatusBadGateway)
		}
	}
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		set(w)
		if fromEdge {
			writeJSON(w, http.StatusOK, edgeproto.ServersResponse{})
		}
	})
	edge.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
		set(w)
		writeJSON(w, http.StatusNotFound, edgeproto.ErrorBody{Error: string(edgeproto.RefusalUnknownServer)})
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	if _, ok := c.Retiring(); ok {
		t.Fatal("a notice before any answer")
	}
	for _, tc := range retiringCases {
		mu.Lock()
		value = tc.value
		mu.Unlock()
		if _, err := c.Servers(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got, ok := c.Retiring(); got != tc.want || ok != tc.ok {
			t.Errorf("API answer with %q: Retiring() = %+v, %v; want %+v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
		if _, err := c.Dial(context.Background(), testServerID); err == nil {
			t.Fatal("Dial of an unknown server succeeded")
		}
		if got, ok := c.Retiring(); got != tc.want || ok != tc.ok {
			t.Errorf("refused connection with %q: Retiring() = %+v, %v; want %+v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}

	mu.Lock()
	value = "https://edge2.example.test"
	mu.Unlock()
	if _, err := c.Servers(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	value, fromEdge = "", false
	mu.Unlock()
	if _, err := c.Servers(context.Background()); err == nil {
		t.Fatal("a proxy's 502 was not an error")
	}
	if got, ok := c.Retiring(); !ok || got.Replacement != "https://edge2.example.test" {
		t.Errorf("an answer without the edge's protocol version changed the notice: %+v, %v", got, ok)
	}
}

func TestRetiringNotice(t *testing.T) {
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		retiring, date string
		want           Retiring
		ok             bool
	}{
		{"", "", Retiring{}, false},
		{"", "2027-03-01", Retiring{}, false},
		{"https://edge2.example.test", "", Retiring{Replacement: "https://edge2.example.test"}, true},
		{"https://edge2.example.test", "2027-03-01", Retiring{Replacement: "https://edge2.example.test", Date: "2027-03-01"}, true},
		{"https://edge2.example.test", "2027-13-01", Retiring{}, false},
		{"https://edge2.example.test/", "", Retiring{}, false},
		{"https://edge2.example.test; date=2027-03-01", "", Retiring{}, false},
		{"javascript:alert(1)", "", Retiring{}, false},
		{"https://edge2.example.test\x1b]0;owned\x07", "", Retiring{}, false},
		{"https://edge2.example.test", "2027-03-01\x1b[2J", Retiring{}, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, edgeproto.EdgeInfo{
				SigninOrigin: "http://127.0.0.1:1", Key: key, Fingerprint: edgeproto.EdgeKeyFingerprint(key),
				Version: edgeproto.Version, MinVersion: edgeproto.MinVersion, Retiring: tc.retiring, RetiringDate: tc.date,
			})
		}))
		c, err := New(t.TempDir(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := c.RetiringNotice(context.Background())
		srv.Close()
		if err != nil || got != tc.want || ok != tc.ok {
			t.Errorf("edge info retiring %q date %q: %+v, %v, %v; want %+v, %v", tc.retiring, tc.date, got, ok, err, tc.want, tc.ok)
		}
	}
}
