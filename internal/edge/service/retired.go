package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	edgestore "github.com/3xDevOps/cloudfield/internal/edge/store"
)

// Retired is a pair taken out of the served pairs: every request to its
// hosts gets 410 Gone naming Replacement, the relay origin of a served
// pair.
type Retired struct {
	SigninOrigin, RelayOrigin, Replacement string
}

// Kinds of request a retired host counts, by path.
const (
	RetiredServer    = "server"
	RetiredClient    = "client"
	RetiredConnector = "connector"
	RetiredBrowser   = "browser"
)

var retiredKinds = []string{RetiredServer, RetiredClient, RetiredConnector, RetiredBrowser}

// retiredCSP lets the retired page load nothing.
const retiredCSP = "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// retiredHost is one host of a retired pair, with its answers rendered at
// start, so no answer depends on the request.
type retiredHost struct {
	origin string
	// short is body without the commands, for a WebSocket upgrade, whose
	// refusal a client reads only 1024 bytes of.
	body, short, page, join []byte
	requests                [4]atomic.Uint64
}

// RetiredRequest counts requests to one host of a retired pair.
type RetiredRequest struct {
	Origin, Kind string
	Count        uint64
}

// newRetired checks the retired pairs against the served pairs, renders
// their answers, and returns them with canonical origins.
func (s *Service) newRetired(retired []Retired) ([]Retired, error) {
	byRelay := map[string]*pair{}
	for _, p := range s.pairs {
		byRelay[p.relay] = p
	}
	s.retired = map[string]*retiredHost{}
	out := make([]Retired, 0, len(retired))
	for _, r := range retired {
		repl := byRelay[r.Replacement]
		if repl == nil || repl.retiring != nil {
			return nil, fmt.Errorf("edge: retired %s in favour of %s: the replacement must be the relay origin of a pair this edge serves that is not retiring", r.RelayOrigin, r.Replacement)
		}
		c := Retired{Replacement: repl.relay}
		for i, o := range []string{r.SigninOrigin, r.RelayOrigin} {
			origin, err := edgeproto.Origin(o)
			if err != nil {
				return nil, fmt.Errorf("edge: retired origin: %w", err)
			}
			host := edgeproto.OriginHost(origin)
			if s.byHost[host] != nil || s.retired[host] != nil {
				return nil, fmt.Errorf("edge: the retired origin %s has the host name %s of another origin; each host name is served or retired once", origin, host)
			}
			signin := i == 0
			h, err := s.retiredAnswers(origin, repl, signin)
			if err != nil {
				return nil, err
			}
			s.retired[host] = h
			s.retiredHosts = append(s.retiredHosts, h)
			if signin {
				c.SigninOrigin = origin
			} else {
				c.RelayOrigin = origin
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// retiredAnswers renders what the host of origin, a retired sign-in
// origin when signin is set, answers: the JSON error and the pages,
// which name repl.
func (s *Service) retiredAnswers(origin string, repl *pair, signin bool) (*retiredHost, error) {
	h := &retiredHost{origin: origin}
	body, err := json.Marshal(edgeproto.ErrorBody{
		Error: fmt.Sprintf("%s is retired; this edge is at %s. Move a server with: cloudfield-server edge move %s. Sign a device in with: cloudfield login --edge %s",
			origin, repl.relay, repl.relay, repl.relay),
		Retired: &edgeproto.Retired{Replacement: repl.relay},
	})
	if err != nil {
		return nil, err
	}
	h.body = append(body, '\n')
	if h.short, err = json.Marshal(edgeproto.ErrorBody{
		Error:   fmt.Sprintf("%s is retired; this edge is at %s", origin, repl.relay),
		Retired: &edgeproto.Retired{Replacement: repl.relay},
	}); err != nil {
		return nil, err
	}
	h.short = append(h.short, '\n')
	address := repl.relay
	if signin {
		address = repl.signin
	}
	for _, page := range []*[]byte{&h.page, &h.join} {
		var buf bytes.Buffer
		if err = s.pages.ExecuteTemplate(&buf, "retired", struct {
			Old, Address, Relay string
			SignIn, Join        bool
		}{origin, address, repl.relay, signin, page == &h.join}); err != nil {
			return nil, fmt.Errorf("edge: render the retired page of %s: %w", origin, err)
		}
		*page = buf.Bytes()
	}
	return h, nil
}

// deleteRetiredSignIns deletes the device tokens, sessions and pending
// device authorizations whose recorded sign-in origin has the host name of
// a retired sign-in origin, whatever its port, and those with no recorded
// origin. It refuses a retired pair whose origins contradict the pairs the
// edge recorded, and rows without an origin when the edge never recorded
// origins, since every row would have none.
func (s *Service) deleteRetiredSignIns(ctx context.Context, path string, retired []Retired) error {
	rep, err := edgestore.ReadRetiring(ctx, path, s.now())
	if err != nil {
		return err
	}
	signinHosts := map[string]bool{}
	for _, r := range retired {
		signinHosts[originHost(r.SigninOrigin)] = true
		for _, o := range rep.Origins {
			relay, signin := originHost(o.RelayOrigin), originHost(o.SigninOrigin)
			switch {
			case relay == originHost(r.SigninOrigin) || signin == originHost(r.RelayOrigin):
				return fmt.Errorf("edge: --retired-pairs names %s as a sign-in origin and %s as a relay origin, but the edge served them the other way round, "+
					"in the pair %s %s: write <sign-in origin> <relay origin> <replacement relay origin>", r.SigninOrigin, r.RelayOrigin, o.SigninOrigin, o.RelayOrigin)
			case relay == originHost(r.RelayOrigin) && signin != originHost(r.SigninOrigin):
				return fmt.Errorf("edge: --retired-pairs pairs %s with %s, but the edge served it with the sign-in origin %s: name that one",
					r.RelayOrigin, r.SigninOrigin, o.SigninOrigin)
			}
		}
	}
	without, err := s.store.SignInsWithoutOrigin(ctx)
	if err != nil {
		return err
	}
	if n := without.Devices + without.Sessions + without.Authorizations; n > 0 && rep.RecordingSince.IsZero() {
		return fmt.Errorf("edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens %d, browser sessions %d, "+
			"pending device sign-ins %d), since this edge has never recorded which pair issued them. Run it first with --retiring-pairs naming the pair, for as long as you give "+
			"people to move, such as 30 days: each token or session used in that time is recorded and kept. A shorter run lets this start pass "+
			"but signs out everyone who did not use the edge during it", without.Devices, without.Sessions, without.Authorizations)
	}
	var origins []string
	for _, r := range retired {
		origins = append(origins, r.SigninOrigin)
	}
	for _, o := range rep.Origins {
		if signinHosts[originHost(o.SigninOrigin)] {
			origins = append(origins, o.SigninOrigin)
		}
	}
	for _, u := range rep.Unpaired {
		if signinHosts[originHost(u.SigninOrigin)] {
			origins = append(origins, u.SigninOrigin)
		}
	}
	n, err := s.store.DeleteSignIns(ctx, origins)
	if err != nil {
		return err
	}
	slog.Info("edge: deleted the sign-ins of retired pairs and those with no recorded origin",
		"devices", n.Devices, "sessions", n.Sessions, "authorizations", n.Authorizations)
	return nil
}

// originHost is the host name of origin as canonicalHost writes it, ""
// when origin does not parse.
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return canonicalHost(u.Hostname())
}

// retiredKind is the kind of request path is, for the retired request
// count.
func retiredKind(path string) int {
	switch {
	case strings.HasPrefix(path, "/v1/server/"):
		return 0
	case strings.HasPrefix(path, "/v1/"):
		return 1
	case path == edgeproto.PathUniversalMCP || strings.HasPrefix(path, edgeproto.PathUniversalMCP+"/") ||
		strings.HasPrefix(path, "/.well-known/") || path == edgeproto.PathMCPChoose:
		return 2
	}
	return 3
}

// serveRetired answers a request to a retired host: 410 Gone with the
// JSON error, or for a browser page the static page, which also clears
// the edge's cookies on the host.
func (s *Service) serveRetired(w http.ResponseWriter, r *http.Request, h *retiredHost) {
	h.requests[retiredKind(r.URL.Path)].Add(1)
	hdr := w.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	page := r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Upgrade") == "" &&
		strings.Contains(strings.Join(r.Header.Values("Accept"), ","), "text/html")
	if !page {
		hdr.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
		hdr.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		if r.Header.Get("Upgrade") != "" || strings.HasPrefix(r.URL.Path, "/v1/server/") || strings.HasPrefix(r.URL.Path, "/v1/connect/") {
			_, _ = w.Write(h.short)
			return
		}
		_, _ = w.Write(h.body)
		return
	}
	hdr.Set("Content-Security-Policy", retiredCSP)
	hdr.Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(s.pairs[0].signin, "https://") {
		hdr.Set("Strict-Transport-Security", "max-age=63072000")
	}
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	for _, name := range []string{sessionCookie, signinCookie, browserCookie} {
		setCookie(w, name, "", -1)
	}
	w.WriteHeader(http.StatusGone)
	if strings.HasPrefix(r.URL.Path, "/join/") {
		_, _ = w.Write(h.join)
		return
	}
	_, _ = w.Write(h.page)
}

// RetiredRequests counts, by host origin and kind, the requests that
// reached a retired host since the edge started.
func (s *Service) RetiredRequests() []RetiredRequest {
	var out []RetiredRequest
	for _, h := range s.retiredHosts {
		for i, kind := range retiredKinds {
			out = append(out, RetiredRequest{Origin: h.origin, Kind: kind, Count: h.requests[i].Load()})
		}
	}
	return out
}
