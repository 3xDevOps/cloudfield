package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// retiredHost answers every request with 410 and text, as a retired pair
// does, or as anyone holding the old host name can.
func retiredHost(t *testing.T, text string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(edgeproto.ErrorBody{Error: text})
	}))
	t.Cleanup(srv.Close)
	origin, err := edgeproto.Origin(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

// moveAgentAt is the agent edge move builds for a server at from that
// pinned pinned, nil for none.
func moveAgentAt(t *testing.T, from string, pinned ed25519.PublicKey) *Agent {
	t.Helper()
	a, err := New(Config{EdgeURL: from, DataDir: t.TempDir(), HostKey: newHostKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	if pinned != nil {
		if err := a.state.Pin(pinned); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func signedNotices(e *fakeEdge, key ed25519.PrivateKey, list edgeproto.Notices) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notices = func(id string, nonce []byte) (edgeproto.Notices, []byte) {
		if key == nil {
			return list, nil
		}
		return list, edgeproto.SignNotices(key, id, e.origin, nonce, list)
	}
}

// A server whose address answers the control dial with 410 moves only
// when the target's own probe carries a retired notice for that address,
// signed with the pinned key for this probe, that names the target. A
// 410, its text and the server's stored notices prove nothing.
func TestMoveFromARetiredAddress(t *testing.T) {
	text := "the address is retired; this edge is at another"
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		// notices are what the target sends, signed with key (the
		// edge's own when nil) or unsigned when unsigned is set.
		notices  func(from, to string) edgeproto.Notices
		key      ed25519.PrivateKey
		unsigned bool
		// stored are notices the server holds from before.
		stored   func(from, to string) []edgeproto.Notice
		version  int
		announce func(from, to string) []string
		text     string
		want     func(from, to string) string
	}{
		{name: "a signed retired notice", notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
		}},
		{name: "no notice, a retired one stored", stored: func(from, to string) []edgeproto.Notice {
			return []edgeproto.Notice{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
		}, want: noRetiredNotice(text)},
		{name: "an unsigned notice", unsigned: true, notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
		}, want: noRetiredNotice(text)},
		{name: "a notice signed with another key", key: other, notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
		}, want: noRetiredNotice(text)},
		{name: "a retiring notice", notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: from, Replacement: to}}
		}, want: noRetiredNotice(text)},
		{name: "control characters in the refusal", text: "gone\r\x1b[2K\nmoved", want: noRetiredNotice("gone??[2K?moved")},
		{name: "another replacement", notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: "https://edge3.example.test"}}
		}, want: func(from, to string) string {
			return fmt.Sprintf("the edge at %s says, signed with the pinned key, that %s is retired in favour of https://edge3.example.test; "+
				"move there instead: edge move https://edge3.example.test", to, from)
		}},
		{name: "protocol 5", version: edgeproto.VersionRelayOrigin - 1, notices: func(from, to string) edgeproto.Notices {
			return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
		}, want: func(_, to string) string {
			return fmt.Sprintf("%s speaks edge protocol 5; moving needs edge protocol 6 or later: the edge's operator upgrades it", to)
		}},
		{name: "one served pair, no relay origins announced", announce: func(string, string) []string { return nil },
			notices: func(from, to string) edgeproto.Notices {
				return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
			}},
		{name: "the target not announced", announce: func(string, string) []string { return []string{"https://edge3.example.test"} },
			notices: func(from, to string) edgeproto.Notices {
				return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
			}, want: func(_, to string) string {
				return fmt.Sprintf("the edge at %s announces the relay addresses https://edge3.example.test; moving needs %s among them: "+
					"the edge's operator adds the address first", to, to)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := text
			if tc.text != "" {
				refusal = tc.text
			}
			from := retiredHost(t, refusal)
			edge := newFakeEdge(t)
			to := edge.origin
			edge.version = tc.version
			edge.relayOrigins = []string{to}
			if tc.announce != nil {
				edge.relayOrigins = tc.announce(from, to)
			}
			if tc.notices != nil {
				key := edge.priv
				if tc.key != nil {
					key = tc.key
				}
				if tc.unsigned {
					key = nil
				}
				signedNotices(edge, key, tc.notices(from, to))
			}
			a := moveAgentAt(t, from, edge.pub)
			if tc.stored != nil {
				p, err := a.state.pinned()
				if err != nil {
					t.Fatal(err)
				}
				if err := a.state.writeNotices(p, StoredNotices{ServerID: a.serverID, PinID: p.ID, Received: time.Now(), Notices: tc.stored(from, to)}); err != nil {
					t.Fatal(err)
				}
				if got, err := a.Notices(); err != nil || len(got.Notices) == 0 {
					t.Fatalf("stored notices %+v, %v", got, err)
				}
			}
			got, retired, err := a.Move(t.Context(), to)
			if tc.want == nil {
				if err != nil || got != to || !retired {
					t.Fatalf("move to %s: %q, %v, %v; want it, from a retired address", to, got, retired, err)
				}
				return
			}
			if want := tc.want(from, to); err == nil || err.Error() != want {
				t.Fatalf("move: %v\nwant: %s", err, want)
			}
		})
	}
}

func noRetiredNotice(text string) func(from, to string) string {
	return func(from, to string) string {
		return fmt.Sprintf("dial %s/v1/server/control: 410 Gone: %s; the edge at %s sends no notice signed with the pinned key "+
			"that %s is retired, and a refusal alone does not show it, so nothing changed", from, text, to, from)
	}
}

// A 410 at the old address still needs the pinned key at the target.
func TestMoveFromARetiredAddressNeedsThePinnedKey(t *testing.T) {
	from := retiredHost(t, "retired")
	edge := newFakeEdge(t)
	edge.relayOrigins = []string{edge.origin}
	signedNotices(edge, edge.priv, edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: edge.origin}})
	pinned, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = moveAgentAt(t, from, pinned).Move(t.Context(), edge.origin)
	if want := fmt.Sprintf("%s presents edge key %s, but this server pinned %s for its edge at %s, which announced it: another edge answers there. "+
		"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
		edge.origin, edgeproto.EdgeKeyFingerprint(edge.pub), edgeproto.EdgeKeyFingerprint(pinned), from); err == nil || err.Error() != want {
		t.Fatalf("move: %v\nwant: %s", err, want)
	}
}

// A redirect at the old address is a refusal like any status but 410:
// the probe follows none, and the target is never asked.
func TestMoveFollowsNoRedirect(t *testing.T) {
	edge := newFakeEdge(t)
	edge.relayOrigins = []string{edge.origin}
	signedNotices(edge, edge.priv, nil)
	for _, status := range []int{http.StatusFound, http.StatusPermanentRedirect} {
		srv := httptest.NewServer(http.RedirectHandler(edge.origin+edgeproto.PathServerControl, status))
		t.Cleanup(srv.Close)
		_, _, err := moveAgentAt(t, srv.URL, edge.pub).Move(t.Context(), edge.origin)
		if want := fmt.Sprintf("edgeagent: enroll at %s: dial %s/v1/server/control: %d %s: Location: %s/v1/server/control",
			srv.URL, srv.URL, status, http.StatusText(status), edge.origin); err == nil || err.Error() != want {
			t.Errorf("redirect %d: %v, want %q", status, err, want)
		}
	}
	select {
	case h := <-edge.hellos:
		t.Fatalf("the target was probed: %+v", h)
	default:
	}
}

// With no key pinned, or no answer at the old address, nothing changes
// and the refusal gives the manual steps.
func TestMoveGivesTheManualSteps(t *testing.T) {
	edge := newFakeEdge(t)
	manual := func(from string) string {
		return "If " + from + " no longer answers for this edge: confirm " + edge.origin + " and the edge key's fingerprint with whoever runs the edge, " +
			"then run sudo cloudfield-server config set edge-url " + edge.origin + " and sudo systemctl restart aether-server"
	}
	from := retiredHost(t, "retired")
	_, _, err := moveAgentAt(t, from, nil).Move(t.Context(), edge.origin)
	if want := "no edge key is pinned for " + from + " yet, so nothing shows that " + edge.origin + " is the same edge; " +
		"start the server, let it connect once, and run edge move again. " + manual(from); err == nil || err.Error() != want {
		t.Errorf("no key pinned: %v\nwant: %s", err, want)
	}
	const silent = "http://127.0.0.1:1"
	_, _, err = moveAgentAt(t, silent, edge.pub).Move(t.Context(), edge.origin)
	if err == nil || !strings.HasPrefix(err.Error(), "edgeagent: enroll at "+silent+": dial "+silent+"/v1/server/control: ") ||
		!strings.HasSuffix(err.Error(), ". "+manual(silent)) || strings.Contains(err.Error(), "\n") {
		t.Errorf("no answer: %v", err)
	}
}

// A target whose own probe names it retiring is refused with its
// replacement; an unsigned notice saying so changes nothing.
func TestMoveRefusesARetiringTarget(t *testing.T) {
	from := newFakeEdge(t)
	to := newFakeEdgeWithKey(t, from.priv)
	for _, e := range []*fakeEdge{from, to} {
		e.relayOrigins = []string{from.origin, to.origin, "https://edge3.example.test"}
	}
	for _, tc := range []struct {
		name string
		key  ed25519.PrivateKey
		date string
		want string
	}{
		{name: "signed", key: from.priv, want: fmt.Sprintf("the edge at %s says, signed with the pinned key, that %s is retiring; "+
			"move there instead: edge move https://edge3.example.test", to.origin, to.origin)},
		{name: "signed, with a date", key: from.priv, date: "2027-03-01", want: fmt.Sprintf("the edge at %s says, signed with the pinned key, "+
			"that %s is retiring on 2027-03-01; move there instead: edge move https://edge3.example.test", to.origin, to.origin)},
		{name: "unsigned"},
	} {
		signedNotices(to, tc.key, edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: to.origin, Replacement: "https://edge3.example.test", Date: tc.date}})
		got, retired, err := moveAgentAt(t, from.origin, from.pub).Move(t.Context(), to.origin)
		if tc.want == "" {
			if err != nil || got != to.origin || retired {
				t.Errorf("%s: %q, %v, %v; want the move", tc.name, got, retired, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v\nwant: %s", tc.name, err, tc.want)
		}
	}
}

// records keeps the warnings logged through slog.
type records struct {
	mu   sync.Mutex
	msgs []string
}

func (r *records) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (r *records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, rec.Message)
	return nil
}
func (r *records) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *records) WithGroup(string) slog.Handler      { return r }

func (r *records) noticeLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for _, m := range r.msgs {
		if strings.Contains(m, "move this server") {
			lines = append(lines, m)
		}
	}
	return lines
}

// Each connect logs one warning when the edge's verified notices name the
// server's own address, and none otherwise.
func TestConnectWarnsOfARetiringAddress(t *testing.T) {
	logs := &records{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })
	edge := newFakeEdge(t)
	cfg := Config{EdgeURL: edge.srv.URL, DataDir: t.TempDir(), SSH: newFakeSSH(), HostKey: newHostKey(t)}
	connect := func(list edgeproto.Notices) []string {
		t.Helper()
		logs.mu.Lock()
		logs.msgs = nil
		logs.mu.Unlock()
		signedNotices(edge, edge.priv, list)
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		stop, _ := run(t, a)
		ec := edge.nextControl(t)
		ec.send(edgeproto.Ping{})
		expect[edgeproto.Pong](t, ec)
		stop()
		return logs.noticeLines()
	}
	for _, tc := range []struct {
		name string
		list edgeproto.Notices
		want []string
	}{
		{"no notice", nil, nil},
		{"another address", edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: "https://edge0.example.test", Replacement: "https://edge2.example.test"}}, nil},
		{"retiring", edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test"}},
			[]string{"edge: " + edge.origin + " is retiring; move this server with: sudo cloudfield-server edge move https://edge2.example.test"}},
		{"retiring on a date", edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}},
			[]string{"edge: " + edge.origin + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move https://edge2.example.test"}},
	} {
		if got := connect(tc.list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: warnings %q, want %q", tc.name, got, tc.want)
		}
	}
}
