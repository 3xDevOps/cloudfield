package edgeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// longHost is a DNS name of the longest legal length, 253 bytes.
var longHost = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)

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
	{"https://edge2.example.test:" + strings.Repeat("0", 400) + "1", Retiring{}, false},
	{"https://" + longHost + ":65535", Retiring{Replacement: "https://" + longHost + ":65535"}, true},
}

// TestRetiringHeader checks every value of Aether-Edge-Retiring an API
// answer may carry, and that an answer from something in between changes
// nothing.
func TestRetiringHeader(t *testing.T) {
	var mu sync.Mutex
	value, fromEdge := "", true
	set := func(w http.ResponseWriter) {
		mu.Lock()
		defer mu.Unlock()
		for v := range strings.SplitSeq(value, "\x00") {
			if v != "" {
				w.Header().Add(edgeproto.HeaderRetiring, v)
			}
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

	mu.Lock()
	value, fromEdge = "https://edge2.example.test\x00https://edge3.example.test", true
	mu.Unlock()
	if _, err := c.Servers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Retiring(); ok {
		t.Errorf("an answer with two %s values gave the notice %+v", edgeproto.HeaderRetiring, got)
	}
}

// TestRetiringHeaderOnRelayedConnections checks the header on the answer
// to a relayed connection, accepted and refused, each from a client that
// has seen no other answer.
func TestRetiringHeaderOnRelayedConnections(t *testing.T) {
	var mu sync.Mutex
	value, accept := "", false
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v, ok := value, accept
		mu.Unlock()
		if v != "" {
			w.Header().Set(edgeproto.HeaderRetiring, v)
		}
		w.Header().Set(edgeproto.HeaderVersion, "1")
		if !ok {
			writeJSON(w, http.StatusNotFound, edgeproto.ErrorBody{Error: string(edgeproto.RefusalUnknownServer)})
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err == nil {
			_ = c.CloseNow()
		}
	})
	for _, accepted := range []bool{true, false} {
		for _, tc := range retiringCases {
			mu.Lock()
			value, accept = tc.value, accepted
			mu.Unlock()
			c := newTestClient(t, edge)
			signIn(t, c)
			conn, err := c.Dial(context.Background(), testServerID)
			if (err == nil) != accepted {
				t.Fatalf("accepted %v: Dial = %v", accepted, err)
			}
			if conn != nil {
				_ = conn.Close()
			}
			if got, ok := c.Retiring(); got != tc.want || ok != tc.ok {
				t.Errorf("accepted %v, %q: Retiring() = %+v, %v; want %+v, %v", accepted, tc.value, got, ok, tc.want, tc.ok)
			}
		}
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
		{"https://edge2.example.test:" + strings.Repeat("0", 400) + "1", "", Retiring{}, false},
		{"https://" + longHost + ":65535", "2027-03-01", Retiring{Replacement: "https://" + longHost + ":65535", Date: "2027-03-01"}, true},
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

// TestRetiringNoticeOfAnotherType reads edge metadata whose retiring
// fields are not strings: the metadata is read and the notice ignored.
func TestRetiringNoticeOfAnotherType(t *testing.T) {
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := map[string]any{"signin_origin": "http://127.0.0.1:1", "key": key, "fingerprint": edgeproto.EdgeKeyFingerprint(key),
			"version": edgeproto.Version, "min_version": edgeproto.MinVersion, "retiring": map[string]string{"origin": "https://edge2.example.test"}, "retiring_date": 20270301}
		w.Header().Set(edgeproto.HeaderVersion, "1")
		_ = json.NewEncoder(w).Encode(info)
	}))
	t.Cleanup(srv.Close)
	c, err := New(t.TempDir(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := c.RetiringNotice(context.Background()); err != nil || ok {
		t.Fatalf("RetiringNotice = %+v, %v, %v; want no notice and no error", got, ok, err)
	}
}
