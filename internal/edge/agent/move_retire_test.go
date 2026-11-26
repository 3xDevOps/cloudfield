package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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
				"move there instead: sudo cloudfield-server edge move https://edge3.example.test", to, from)
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
		{name: "only unusable relay origins announced", announce: func(string, string) []string { return []string{"ftp://edge3.example.test"} },
			notices: func(from, to string) edgeproto.Notices {
				return edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
			}, want: func(_, to string) string {
				return fmt.Sprintf("the edge at %s announces no relay address this server can use; moving needs %s among them: "+
					"the edge's operator adds the address first", to, to)
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
				held, err := a.state.HoldPin()
				if err != nil {
					t.Fatal(err)
				}
				err = a.state.writeNotices(held.p, StoredNotices{ServerID: a.serverID, Received: time.Now(), Notices: tc.stored(from, to)})
				held.Close()
				if err != nil {
					t.Fatal(err)
				}
				if got, err := a.Notices(); err != nil || len(got.Notices) == 0 {
					t.Fatalf("stored notices %+v, %v", got, err)
				}
			}
			got, err := moveHeld(t, a, to)
			if tc.want == nil {
				if want := (Moved{To: to, Retired: true}); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("move to %s: %+v, %v; want %+v", to, got, err, want)
				}
				return
			}
			if want := tc.want(from, to); err == nil || err.Error() != want {
				t.Fatalf("move: %v\nwant: %s", err, want)
			}
		})
	}
}

// moveHeld runs a.Move under the pin in place, as edge move does.
func moveHeld(t *testing.T, a *Agent, target string) (Moved, error) {
	t.Helper()
	held, err := a.state.HoldPin()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	return a.Move(t.Context(), target, held, testCommands)
}

// testCommands spell the commands as an admin types them under sudo with
// the default config file.
var testCommands = Commands{
	Server:  func(args string) string { return "sudo cloudfield-server " + args },
	Restart: "sudo systemctl restart aether-server",
}

func manualSteps(from, to string) string {
	return "If " + from + " no longer answers for this edge: confirm " + to + " and the edge key's fingerprint with whoever runs the edge, " +
		"then run sudo cloudfield-server config set edge-url " + to + " and sudo systemctl restart aether-server"
}

func noRetiredNotice(text string) func(from, to string) string {
	return func(from, to string) string {
		return fmt.Sprintf("dial %s/v1/server/control: 410 Gone: %s; the edge at %s sends no notice signed with the pinned key "+
			"that %s is retired, and a refusal alone does not show it, so nothing changed. %s", from, text, to, from, manualSteps(from, to))
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
	_, err = moveHeld(t, moveAgentAt(t, from, pinned), edge.origin)
	if want := fmt.Sprintf("%s presents edge key %s, but this server pinned %s for its edge: another edge answers there. "+
		"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
		edge.origin, edgeproto.EdgeKeyFingerprint(edge.pub), edgeproto.EdgeKeyFingerprint(pinned)); err == nil || err.Error() != want {
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
		_, err := moveHeld(t, moveAgentAt(t, srv.URL, edge.pub), edge.origin)
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
// and the refusal gives the manual steps; an answer that fails TLS or the
// protocol gets none.
func TestMoveGivesTheManualSteps(t *testing.T) {
	edge := newFakeEdge(t)
	from := retiredHost(t, "retired")
	_, err := moveHeld(t, moveAgentAt(t, from, nil), edge.origin)
	if want := "no edge key is pinned for " + from + " yet, so nothing shows that " + edge.origin + " is the same edge; " +
		"start the server, let it connect once, and run edge move again. If " + from + " no longer answers for this edge: confirm " + edge.origin +
		" and the edge key's fingerprint with whoever runs the edge, then run sudo cloudfield-server config set edge-url " + edge.origin +
		", sudo cloudfield-server edge trust (it shows the fingerprint to compare; type yes) and sudo systemctl restart aether-server"; err == nil || err.Error() != want {
		t.Errorf("no key pinned: %v\nwant: %s", err, want)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusing := "http://" + closed.Addr().String()
	_ = closed.Close()
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	held := make(chan net.Conn, 4)
	t.Cleanup(func() {
		close(held)
		for c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()
	tlsHost := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsHost.Close)
	for _, tc := range []struct {
		name, from string
		steps      bool
	}{
		{"connection refused", refusing, true},
		{"no handshake in time", "http://" + silent.Addr().String(), true},
		{"an untrusted certificate", tlsHost.URL, false},
	} {
		_, err := moveHeld(t, moveAgentAt(t, tc.from, edge.pub), edge.origin)
		if err == nil || !strings.HasPrefix(err.Error(), "edgeagent: enroll at "+tc.from+": dial "+tc.from+"/v1/server/control: ") ||
			strings.HasSuffix(err.Error(), ". "+manualSteps(tc.from, edge.origin)) != tc.steps || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: %v; want the manual steps: %v", tc.name, err, tc.steps)
		}
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
		{name: "signed", key: from.priv, want: fmt.Sprintf("the edge says, signed with the pinned key, that %s is retiring; "+
			"move there instead: sudo cloudfield-server edge move https://edge3.example.test", to.origin)},
		{name: "signed, with a date", key: from.priv, date: "2027-03-01", want: fmt.Sprintf("the edge says, signed with the pinned key, "+
			"that %s is retiring on 2027-03-01; move there instead: sudo cloudfield-server edge move https://edge3.example.test", to.origin)},
		{name: "unsigned"},
	} {
		signedNotices(to, tc.key, edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: to.origin, Replacement: "https://edge3.example.test", Date: tc.date}})
		got, err := moveHeld(t, moveAgentAt(t, from.origin, from.pub), to.origin)
		if tc.want == "" {
			if want := (Moved{To: to.origin}); err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %+v, %v; want %+v", tc.name, got, err, want)
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
			[]string{"edge: " + edge.origin + " is retiring; move this server with: sudo cloudfield-server edge move https://edge2.example.test (with the service's --config <file>, if it has one)"}},
		{"retiring on a date", edgeproto.Notices{{State: edgeproto.NoticeRetiring, Origin: edge.origin, Replacement: "https://edge2.example.test", Date: "2027-03-01"}},
			[]string{"edge: " + edge.origin + " is retiring on 2027-03-01; move this server with: sudo cloudfield-server edge move https://edge2.example.test (with the service's --config <file>, if it has one)"}},
	} {
		if got := connect(tc.list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: warnings %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Either probe's verified notices that name the target retiring or
// retired refuse the move; a replacement that is where the server already
// is says so. A move off an address the notices name retiring says it.
func TestMoveReadsBothProbesNotices(t *testing.T) {
	from := newFakeEdge(t)
	to := newFakeEdgeWithKey(t, from.priv)
	for _, e := range []*fakeEdge{from, to} {
		e.relayOrigins = []string{from.origin, to.origin, "https://edge3.example.test"}
	}
	for _, tc := range []struct {
		name     string
		at       *fakeEdge
		notice   edgeproto.Notice
		want     string
		retiring bool
	}{
		{name: "the target retired, from the first probe", at: from,
			notice: edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: to.origin, Replacement: "https://edge3.example.test"},
			want:   "the edge says, signed with the pinned key, that " + to.origin + " is retired; move there instead: sudo cloudfield-server edge move https://edge3.example.test"},
		{name: "the target retiring in favour of the address in use", at: to,
			notice: edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: to.origin, Replacement: from.origin},
			want:   "the edge says, signed with the pinned key, that " + to.origin + " is retiring and names " + from.origin + ", where this server already is; nothing changed"},
		{name: "the address in use signed retired while it still answers", at: from,
			notice: edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: from.origin, Replacement: to.origin}},
		{name: "the address in use retiring", at: from,
			notice: edgeproto.Notice{State: edgeproto.NoticeRetiring, Origin: from.origin, Replacement: to.origin}, retiring: true},
	} {
		signedNotices(from, nil, nil)
		signedNotices(to, nil, nil)
		signedNotices(tc.at, tc.at.priv, edgeproto.Notices{tc.notice})
		got, err := moveHeld(t, moveAgentAt(t, from.origin, from.pub), to.origin)
		if tc.want != "" {
			if err == nil || err.Error() != tc.want {
				t.Errorf("%s: %v\nwant: %s", tc.name, err, tc.want)
			}
			continue
		}
		if want := (Moved{To: to.origin, Retired: tc.notice.State == edgeproto.NoticeRetired, Retiring: tc.retiring}); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v; want %+v", tc.name, got, err, want)
		}
	}
}

// Move enrolls only under the key pinned when it started and never pins
// one: a pin changed or removed while it runs refuses the move, and
// WhilePinned refuses to write under another pin.
func TestMoveIsBoundToThePinItStartedWith(t *testing.T) {
	edge := newFakeEdge(t)
	other := newFakeEdgeWithKey(t, edge.priv)
	for _, e := range []*fakeEdge{edge, other} {
		e.relayOrigins = []string{edge.origin, other.origin}
	}
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub := otherKey.Public().(ed25519.PublicKey)
	changed := "edgeagent: enroll at " + edge.origin + ": the pinned edge key changed from " + edgeproto.EdgeKeyFingerprint(edge.pub) +
		" while this ran; nothing changed, run it again"
	for _, tc := range []struct {
		name   string
		change func(s *State) error
		after  ed25519.PublicKey
	}{
		{"another key pinned", func(s *State) error { return s.Pin(otherPub) }, otherPub},
		{"the pin removed", func(s *State) error { return os.Remove(s.path(pinFile)) }, nil},
		{"another key, then the same key again", func(s *State) error {
			if err := s.Pin(otherPub); err != nil {
				return err
			}
			return s.Pin(edge.pub)
		}, edge.pub},
	} {
		a := moveAgentAt(t, edge.origin, edge.pub)
		held, err := a.state.HoldPin()
		if err != nil {
			t.Fatal(err)
		}
		edge.mu.Lock()
		edge.notices = func(string, []byte) (edgeproto.Notices, []byte) {
			if err := tc.change(a.state); err != nil {
				t.Error(err)
			}
			return nil, nil
		}
		edge.mu.Unlock()
		if _, err := a.Move(t.Context(), other.origin, held, testCommands); err == nil || err.Error() != changed {
			t.Errorf("%s: %v\nwant: %s", tc.name, err, changed)
		}
		if key, err := a.state.PinnedKey(); err != nil || !key.Equal(tc.after) {
			t.Errorf("%s: pinned %s after the move, %v", tc.name, edgeproto.EdgeKeyFingerprint(key), err)
		}
		wrote := false
		if err := a.state.WhilePinned(held, func() error { wrote = true; return nil }); err == nil || err.Error() != strings.TrimPrefix(changed, "edgeagent: enroll at "+edge.origin+": ") || wrote {
			t.Errorf("%s: WhilePinned under a replaced pin: %v, wrote %v", tc.name, err, wrote)
		}
		held.Close()
	}
}

// An edge that lists relay origins, none of them usable, announces none:
// only one that lists nothing serves a single pair.
func TestMoveFromRetiredNeedsAUsableList(t *testing.T) {
	const from, to = "https://edge-old.example.test", "https://edge-new.example.test"
	refused := &DialError{URL: from + edgeproto.PathServerControl, Status: http.StatusGone}
	retired := []edgeproto.Notice{{State: edgeproto.NoticeRetired, Origin: from, Replacement: to}}
	single := enrollment{protocol: edgeproto.Version, notices: retired}
	if err := checkMoveFromRetired(single, refused, testCommands, from, to); err != nil {
		t.Errorf("one served pair: %v", err)
	}
	unusable := single
	unusable.ready.RelayOrigins = []string{}
	if err := checkMoveFromRetired(unusable, refused, testCommands, from, to); err == nil ||
		err.Error() != "the edge at "+to+" announces no relay address this server can use; moving needs "+to+" among them: the edge's operator adds the address first" {
		t.Errorf("only unusable origins: %v", err)
	}
}

// A target the first probe's notices rule out is refused before anything
// dials it.
func TestMoveDoesNotProbeATargetRuledOut(t *testing.T) {
	from := newFakeEdge(t)
	to := newFakeEdgeWithKey(t, from.priv)
	from.relayOrigins = []string{from.origin, to.origin}
	signedNotices(from, from.priv, edgeproto.Notices{{State: edgeproto.NoticeRetired, Origin: to.origin, Replacement: from.origin}})
	_, err := moveHeld(t, moveAgentAt(t, from.origin, from.pub), to.origin)
	if want := "the edge says, signed with the pinned key, that " + to.origin + " is retired and names " + from.origin +
		", where this server already is; nothing changed"; err == nil || err.Error() != want {
		t.Fatalf("move: %v\nwant: %s", err, want)
	}
	select {
	case h := <-to.hellos:
		t.Fatalf("the target was probed: %+v", h)
	default:
	}
}

// A refusal whose status arrived before the deadline is reported as that
// status, even when its body is still coming when the deadline passes.
func TestDialHonoursAStatusThatArrived(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"ret`))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	_, _, err := dial(t.Context(), srv.URL+edgeproto.PathServerControl, nil, 200*time.Millisecond)
	var refused *DialError
	if !errors.As(err, &refused) || refused.Status != http.StatusGone || errors.Is(err, errNoAnswer) {
		t.Fatalf("dial: %v, want a 410 refusal", err)
	}
}

// A stored entry that would not pass in a ready is no notice.
func TestStoredNoticesIgnoreDamagedEntries(t *testing.T) {
	const own = "https://edge-old.example.test"
	for _, n := range []edgeproto.Notice{
		{State: "gone", Origin: own, Replacement: "https://edge-new.example.test"},
		{State: edgeproto.NoticeRetiring, Origin: own, Replacement: "not-a-url"},
		{State: edgeproto.NoticeRetiring, Origin: own, Replacement: "https://edge-new.example.test; rm -rf /"},
		{State: edgeproto.NoticeRetiring, Origin: own, Replacement: own},
		{State: edgeproto.NoticeRetiring, Origin: own, Replacement: "https://edge-new.example.test", Date: "2027-02-30"},
	} {
		if got, ok := (StoredNotices{Notices: []edgeproto.Notice{n}}).For(own); ok {
			t.Errorf("%+v read as %+v", n, got)
		}
	}
}
