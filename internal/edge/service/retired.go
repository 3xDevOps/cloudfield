package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
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
	origin           string
	body, page, join []byte
	requests         [4]atomic.Uint64
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
// device authorizations issued at a retired sign-in origin or with no
// recorded origin.
func (s *Service) deleteRetiredSignIns(ctx context.Context, retired []Retired) error {
	origins := make([]string, 0, 2*len(retired))
	for _, r := range retired {
		origins = append(origins, r.SigninOrigin)
		if base, ok := strings.CutSuffix(r.SigninOrigin, ":443"); ok {
			origins = append(origins, base)
		} else if strings.HasPrefix(r.SigninOrigin, "https://") && !strings.Contains(strings.TrimPrefix(r.SigninOrigin, "https://"), ":") {
			origins = append(origins, r.SigninOrigin+":443")
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
