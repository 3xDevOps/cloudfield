package edgeagent

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/shellquote"
	"github.com/3xDevOps/cloudfield/internal/version"
	"github.com/coder/websocket"
)

// drained ends a session whose edge sent drain as it stopped.
type drained struct{ edgeproto.Drain }

func (drained) Error() string { return "edge is restarting (drain)" }

// wait is the drain's hint: RetryAfterMS plus a random part of JitterMS,
// each clamped to [0, limit] so a hint out of range neither fails the
// message nor parks the server.
func (d drained) wait(limit time.Duration) time.Duration {
	clamp := func(ms int64) time.Duration {
		return time.Duration(min(max(ms, 0), limit.Milliseconds())) * time.Millisecond
	}
	wait := clamp(d.RetryAfterMS)
	if jitter := clamp(d.JitterMS); jitter > 0 {
		wait += rand.N(jitter)
	}
	return wait
}

// session is one enrolled control connection, at protocol version
// protocol.
type session struct {
	c        *websocket.Conn
	edgeKey  ed25519.PublicKey
	protocol int
	// origins are the relay origins requests relayed on the session may
	// come in on: the one the server dialed, then those Ready announced.
	origins []string
	// since is when the session enrolled.
	since time.Time
	// beats holds the latest answered ping recordBeats has not written.
	beats chan time.Time

	mu sync.Mutex
	// transfers holds, by report id, each transfer waiting for the edge's
	// answer.
	transfers map[string]chan edgeproto.OwnerTransferResult
}

func (s *session) send(m edgeproto.Message) error {
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := s.c.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("edgeagent: send: %w", err)
	}
	return nil
}

// DialError is a WebSocket upgrade the edge, or something in between,
// answered with Status instead of 101. Message is the error text its
// body carried, and for a redirect the Location it named, with control
// characters replaced and at most maxDialMessage bytes long.
type DialError struct {
	URL     string
	Status  int
	Message string
}

func (e *DialError) Error() string {
	status := fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
	if e.Message == "" {
		return fmt.Sprintf("dial %s: %s", e.URL, status)
	}
	return fmt.Sprintf("dial %s: %s: %s", e.URL, status, e.Message)
}

// errNoAnswer is a dial nothing answered: no connection could be made, or
// no handshake came in time. A TLS or protocol failure is an answer.
var errNoAnswer = errors.New("no answer")

type noAnswer struct{ error }

func (noAnswer) Is(target error) bool { return target == errNoAnswer }

func (n noAnswer) Unwrap() error { return n.error }

// errNoReady is an enrollment the edge closed after the hello without a
// ready, as a relay does for a signature it does not verify. A ready that
// came but does not decode, a timeout or a cancellation is not one.
var errNoReady = errors.New("no ready")

type noReady struct{ error }

func (noReady) Is(target error) bool { return target == errNoReady }

func (n noReady) Unwrap() error { return n.error }

// noRedirect keeps every dial and probe on the origin it named.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// dial opens a WebSocket that lives until ctx is done or the returned
// cancel is called. Only the handshake is bounded by timeout: net/http ties
// an upgraded connection to its request's context, so a WithTimeout context
// would close the connection when the timer fired. The client honours
// HTTPS_PROXY and follows no redirect; a refused upgrade is a *DialError.
func dial(ctx context.Context, url string, header http.Header, timeout time.Duration) (*websocket.Conn, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(timeout, cancel)
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header, HTTPClient: noRedirect})
	expired := !timer.Stop()
	if err == nil && expired {
		_ = c.CloseNow()
	}
	if err != nil || expired {
		cancel()
		return nil, nil, dialFailure(url, resp, err, expired, timeout)
	}
	return c, cancel, nil
}

// dialFailure is the error of a dial to url that failed with err, or
// whose timer expired. A response that arrived is an answer, whenever the
// timer fired: a refused upgrade is a *DialError, a failed one an error.
func dialFailure(url string, resp *http.Response, err error, expired bool, timeout time.Duration) error {
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		var body edgeproto.ErrorBody
		if resp.Body != nil {
			_ = json.NewDecoder(resp.Body).Decode(&body)
		}
		msg := body.Error
		if loc := resp.Header.Get("Location"); loc != "" && resp.StatusCode/100 == 3 {
			msg = strings.TrimSpace(msg + " Location: " + loc)
		}
		return &DialError{URL: url, Status: resp.StatusCode, Message: printableText(msg, maxDialMessage)}
	}
	if resp != nil {
		if err == nil {
			err = fmt.Errorf("the upgrade completed after %s", timeout)
		}
		return fmt.Errorf("dial %s: %w", url, err)
	}
	if expired {
		return noAnswer{fmt.Errorf("dial %s: no handshake within %s", url, timeout)}
	}
	if op := (*net.OpError)(nil); errors.As(err, &op) && op.Op == "dial" {
		return noAnswer{fmt.Errorf("dial %s: %w", url, err)}
	}
	return fmt.Errorf("dial %s: %w", url, err)
}

func readMessage(ctx context.Context, c *websocket.Conn, timeout time.Duration) (edgeproto.Message, error) {
	for {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		_, data, err := c.Read(rctx)
		expired := rctx.Err() == context.DeadlineExceeded
		cancel()
		if expired {
			return nil, fmt.Errorf("edge silent for %s", timeout)
		}
		if err != nil {
			return nil, err
		}
		m, err := edgeproto.DecodeControl(data)
		if errors.Is(err, edgeproto.ErrUnknownMessage) {
			slog.Debug("edge: skipping unknown control message", "error", err)
			continue
		}
		return m, err
	}
}

// enrollment is what one enrollment proved.
type enrollment struct {
	ready    edgeproto.Ready
	protocol int
	// notices are ready's notices when their signature verified against
	// the pinned key for this server, the origin it dialed and the nonce
	// it drew for this enrollment; nil otherwise.
	notices []edgeproto.Notice
	// pinned is set when this enrollment pinned the edge key, so its
	// notices are trusted on first use, as the pin is.
	pinned bool
	// pin is the pin the notices were verified under, its file held open
	// until the enrollment's owner calls pin.close.
	pin pin
}

// enroll dials the control endpoint at origin and answers the challenge.
// It returns the enrolled connection and what the enrollment proved once
// the edge key matches the pinned one, pinning it on first enrollment.
// With only set, it neither pins nor names a pin, and refuses unless only
// is still the pin file in place and its key the one the edge presents.
func (a *Agent) enroll(ctx context.Context, origin string, only *pin) (*websocket.Conn, context.CancelFunc, enrollment, error) {
	c, cancel, err := dial(ctx, origin+edgeproto.PathServerControl, nil, handshakeTimeout)
	if err != nil {
		return nil, nil, enrollment{}, err
	}
	c.SetReadLimit(edgeproto.MaxControlMessageSize)
	e, err := a.handshake(ctx, c, origin, only)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		cancel()
		return nil, nil, enrollment{}, err
	}
	return c, cancel, e, nil
}

func (a *Agent) handshake(ctx context.Context, c *websocket.Conn, origin string, only *pin) (enrollment, error) {
	m, err := readMessage(ctx, c, handshakeTimeout)
	if err != nil {
		return enrollment{}, fmt.Errorf("read challenge: %w", err)
	}
	ch, ok := m.(edgeproto.Challenge)
	if !ok {
		return enrollment{}, fmt.Errorf("edge sent %T, want a challenge", m)
	}
	if err = edgeproto.CheckVersion(ch.Version); err != nil {
		return enrollment{}, fmt.Errorf("edge speaks protocol %d: %w", ch.Version, err)
	}
	// The signature names the origin this server dialed, never ch.Origin:
	// a challenge relayed from another edge must not enroll there.
	sig, err := edgeproto.SignEnrollment(a.cfg.HostKey, origin, ch.Nonce)
	if err != nil {
		return enrollment{}, err
	}
	noticeNonce := make([]byte, edgeproto.NonceSize)
	_, _ = crand.Read(noticeNonce) // crypto/rand.Read never returns an error.
	protocol := min(edgeproto.Version, ch.Version)
	hello, err := edgeproto.EncodeControl(edgeproto.Hello{
		Version:      protocol,
		HostKey:      a.cfg.HostKey.PublicKey().Marshal(),
		Signature:    sig,
		AgentVersion: version.Version,
		Name:         a.name,
		AccessPolicy: a.policy,
		NoticeNonce:  noticeNonce,
	})
	if err != nil {
		return enrollment{}, err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	err = c.Write(wctx, websocket.MessageText, hello)
	cancel()
	if err != nil {
		return enrollment{}, fmt.Errorf("send hello: %w", err)
	}
	m, err = readMessage(ctx, c, handshakeTimeout)
	if err != nil && ctx.Err() == nil && (websocket.CloseStatus(err) != -1 || errors.Is(err, io.EOF)) {
		return enrollment{}, fmt.Errorf("read ready: %w", noReady{err})
	}
	if err != nil {
		return enrollment{}, fmt.Errorf("read ready: %w", err)
	}
	ready, ok := m.(edgeproto.Ready)
	if !ok {
		return enrollment{}, fmt.Errorf("edge sent %T, want ready", m)
	}
	if ready.ServerID != a.serverID {
		return enrollment{}, fmt.Errorf("edge enrolled server %s, this server is %s", ready.ServerID, a.serverID)
	}
	if only != nil {
		if err = a.state.stillPinned(*only); err != nil {
			return enrollment{}, err
		}
		if !only.Key.Equal(ready.EdgeKey) {
			return enrollment{}, pinMismatch(only.Key, ready.EdgeKey)
		}
		e := enrollment{ready: ready, protocol: protocol}
		if edgeproto.VerifyNotices(ready.EdgeKey, a.serverID, origin, noticeNonce, ready.Notices, ready.NoticesSignature) == nil {
			e.notices = ready.Notices
		}
		return e, nil
	}
	cur, err := a.state.PinnedKey()
	if err != nil {
		return enrollment{}, err
	}
	if cur == nil {
		if err = a.state.refuseEarlierPins(); err != nil {
			return enrollment{}, err
		}
	}
	e := enrollment{ready: ready, protocol: protocol}
	if e.pin, e.pinned, err = a.state.pinFirst(ready.EdgeKey); err != nil {
		return enrollment{}, err
	}
	if e.pinned {
		slog.Info("edge: pinned edge key", "edge", origin, "fingerprint", edgeproto.EdgeKeyFingerprint(ready.EdgeKey))
	}
	if edgeproto.VerifyNotices(ready.EdgeKey, a.serverID, origin, noticeNonce, ready.Notices, ready.NoticesSignature) == nil {
		e.notices = ready.Notices
	}
	return e, nil
}

// session runs one control connection until it ends and returns how long
// it stayed enrolled with an accepted edge key.
func (a *Agent) session(ctx context.Context) (time.Duration, error) {
	c, cancel, e, err := a.enroll(ctx, a.origin, nil)
	if err != nil {
		return 0, err
	}
	defer e.pin.close()
	ready, protocol := e.ready, e.protocol
	defer cancel()
	defer c.CloseNow() //nolint:errcheck // the close error of a dead session is not actionable
	start := time.Now()
	origins := []string{a.origin}
	for _, o := range ready.RelayOrigins {
		if !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	s := &session{c: c, edgeKey: ready.EdgeKey, protocol: protocol, origins: origins, since: start, beats: make(chan time.Time, 1),
		transfers: map[string]chan edgeproto.OwnerTransferResult{}}
	if err = a.setEdgeState(ready.State); err != nil {
		return time.Since(start), err
	}
	if err = a.state.writeNotices(e.pin, StoredNotices{ServerID: a.serverID, Received: start, FirstUse: e.pinned, Notices: e.notices}); err != nil {
		return time.Since(start), err
	}
	if stored, serr := a.state.Notices(a.serverID); serr == nil && !stored.Received.Before(start) {
		if n, ok := stored.For(a.origin); ok {
			slog.Warn("edge: "+NoticeLine(n, "sudo cloudfield-server edge move "+shellquote.Quote(n.Replacement)+
				" (with the service's --config <file>, if it has one) if edge-url there still names "+a.origin), "edge", a.origin, "replacement", n.Replacement, "date", n.Date, "first_use", e.pinned)
		}
	}
	if err = a.reconcileOwner(s, ready.State); err != nil {
		return time.Since(start), err
	}
	// Live only now: a transfer's report must follow the owner reported
	// here, or the edge would record this older one last. The status says
	// connected only once transfers and claims can use the session.
	a.setLive(s)
	defer a.setLive(nil)
	a.state.writeStatus(a.connected(s, time.Now()))
	slog.Info("edge: connected", "edge", a.origin, "server_id", a.serverID, "state", ready.State)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go a.pushDirectory(sctx, s)
	go a.ping(sctx, s)
	recorded := make(chan struct{})
	go func() {
		a.recordBeats(sctx, s)
		close(recorded)
	}()
	err = a.serve(ctx, sctx, s)
	// The disconnected status the caller writes must land after the last
	// heartbeat.
	stop()
	<-recorded
	return time.Since(start), err
}

// reconcileOwner makes the edge's record of this server's owner, state,
// agree with this server's, which only a claim code from this host or a
// transfer sets. An edge without an owner makes the server forget its own.
// An edge recording a claimed server is told the server's owner again: a
// transfer that got no answer may or may not have been recorded there. An
// edge recording an owner the server does not have is told the server is
// ownerless, so a claim code can claim it: the server keeps its owner per
// edge key, and has none for a key `aether-server edge trust` pinned in
// place of another.
func (a *Agent) reconcileOwner(s *session, state string) error {
	if state == edgeproto.StateUnclaimed {
		return a.forgetOwner()
	}
	owner, err := a.state.Owner()
	if err != nil {
		return err
	}
	if owner != nil {
		if err := edgeproto.CheckProvider(owner.Provider); err != nil {
			// A build from the v0.5.2-alpha.3 tag could record a Google
			// account as the owner, which no edge signs in any more.
			slog.Warn("edge: forgetting this server's owner", "edge", a.origin, "error", err)
			return a.disown(s, *owner)
		}
		return s.send(edgeproto.OwnerTransferred{ID: edgeproto.NewConnID(), Owner: edgeproto.AccountPrincipal(*owner)})
	}
	slog.Warn("edge: the edge records an owner this server does not have for its key; reporting the server ownerless. Claim it with a code from `aether-server edge claim-code`",
		"edge", a.origin)
	return s.send(edgeproto.Ownerless{})
}

// forgetOwner drops an owner the edge does not know. The edge records
// ownership only from this server's reports, so its owner removed the
// server there while it was offline, the owner's account was deleted, or
// the edge lost its data. Claiming again takes a new claim code from this
// host, and the server itself still admits only its existing admins as
// the claimant.
func (a *Agent) forgetOwner() error {
	owner, err := a.state.Owner()
	if err != nil || owner == nil {
		return err
	}
	if err := a.state.ClearOwner(); err != nil {
		return err
	}
	slog.Warn("edge: the edge does not know this server's owner; claim it again with a code from `aether-server edge claim-code`",
		"edge", a.origin, "owner_provider", owner.Provider, "owner_subject", owner.Subject)
	return nil
}

func (a *Agent) ping(ctx context.Context, s *session) {
	t := time.NewTicker(a.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.send(edgeproto.Ping{}); err != nil {
				return
			}
		}
	}
}

// connected is the status of live session s whose edge last answered at
// heartbeat.
func (a *Agent) connected(s *session, heartbeat time.Time) Status {
	return Status{Edge: a.origin, Connected: true, Since: s.since, PID: os.Getpid(), Heartbeat: heartbeat, Protocol: s.protocol}
}

// answered queues the heartbeat at now for recordBeats, replacing one it
// has not written yet. Only serve calls it, so the send cannot block.
func (s *session) answered(now time.Time) {
	select {
	case <-s.beats:
	default:
	}
	s.beats <- now
}

// recordBeats writes s's heartbeats to the status file off the read loop,
// which a slow disk must not stall. A run of failed writes logs once.
func (a *Agent) recordBeats(ctx context.Context, s *session) {
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-s.beats:
			err := a.state.write(statusFile, a.connected(s, now))
			if err != nil && !failing {
				slog.Warn("edge: write status heartbeat; later failures are not logged until a write succeeds", "error", err)
			}
			failing = err != nil
		}
	}
}

// EdgeVersion is the protocol version of the enrolled control connection,
// 0 while there is none. Below edgeproto.VersionHTTP the edge does not
// relay MCP connector requests.
func (a *Agent) EdgeVersion() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live == nil {
		return 0
	}
	return a.live.protocol
}

// RelayOrigins are the relay origins connector requests may come in on:
// those the enrolled control connection announced, from
// edgeproto.VersionRelayOrigin, else only the one the server dialed.
func (a *Agent) RelayOrigins() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live == nil || a.live.protocol < edgeproto.VersionRelayOrigin {
		return []string{a.origin}
	}
	return slices.Clone(a.live.origins)
}

// Notices are the verified notices of the server's latest enrollment at
// the edge holding the pinned key.
func (a *Agent) Notices() (StoredNotices, error) { return a.state.Notices(a.serverID) }

// EdgeKey is the key of the edge the control connection is enrolled
// with, nil while there is none.
func (a *Agent) EdgeKey() ed25519.PublicKey {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live == nil {
		return nil
	}
	return a.live.edgeKey
}

func (a *Agent) setLive(s *session) {
	a.mu.Lock()
	a.live = s
	a.mu.Unlock()
}

func (a *Agent) setEdgeState(state string) error {
	a.edgeStateMu.Lock()
	defer a.edgeStateMu.Unlock()
	a.mu.Lock()
	a.edgeReports++
	a.edgeState = state
	a.mu.Unlock()
	return a.state.writeEdgeState(a.serverID, state)
}

// setClaimed records the claim reported when edgeReports was reports,
// unless a state was recorded since: that one is newer than the claim.
func (a *Agent) setClaimed(reports uint64) error {
	a.edgeStateMu.Lock()
	defer a.edgeStateMu.Unlock()
	a.mu.Lock()
	stale := a.edgeReports != reports
	if !stale {
		a.edgeState = edgeproto.StateClaimed
	}
	a.mu.Unlock()
	if stale {
		return nil
	}
	return a.state.writeEdgeState(a.serverID, edgeproto.StateClaimed)
}

// pushDirectory sends the directory whenever it may have changed, and
// re-reads it every directoryRefresh for changes another process made,
// such as `aether-server device approve` accepting an invitation. An
// unchanged directory is not sent again. When the owner's identity is no
// longer a member's, the server is ownerless: the agent forgets the owner
// and tells the edge.
func (a *Agent) pushDirectory(ctx context.Context, s *session) {
	var sent []edgeproto.DirectoryEntry
	for {
		// The owner is read before the directory: a claim or a transfer
		// records an owner only once its member is in the directory.
		owner, err := a.state.Owner()
		var entries []edgeproto.DirectoryEntry
		if err == nil {
			entries, err = a.cfg.SSH.EdgeDirectory(ctx)
		}
		if err == nil && (sent == nil || !slices.Equal(sent, entries)) {
			if err = s.send(edgeproto.Directory{Entries: entries}); err == nil {
				sent = append(make([]edgeproto.DirectoryEntry, 0, len(entries)), entries...)
			}
		}
		if err == nil && owner != nil && !isMember(entries, *owner) {
			err = a.disown(s, *owner)
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("edge: push directory", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.cfg.SSH.EdgeDirectoryChanged():
		case <-time.After(a.directoryRefresh):
		}
	}
}

func (a *Agent) serve(run, ctx context.Context, s *session) error {
	for {
		m, err := readMessage(ctx, s.c, a.idleTimeout)
		if err != nil {
			return fmt.Errorf("control connection: %w", err)
		}
		// Ending run closes the relayed connections, and the edge can send
		// an open in their place before ctx, derived from run, ends. Their
		// slots free only as they finish closing, so an answer could refuse
		// it as over the limit; unanswered, the edge holds it.
		if err := run.Err(); err != nil {
			return fmt.Errorf("control connection: server stopping: %w", err)
		}
		switch m := m.(type) {
		case edgeproto.Ping:
			if err := s.send(edgeproto.Pong{}); err != nil {
				return err
			}
		case edgeproto.Pong:
			s.answered(time.Now())
		case edgeproto.Open:
			a.open(s, m)
		case edgeproto.DeviceRevoked:
			a.revokeDevice(m.DeviceID)
		case edgeproto.OwnerTransferResult:
			s.transferAnswered(m)
		case edgeproto.AccountDeleted:
			// Unanswered, the edge sends the deletion again at the next
			// enrollment.
			if err := a.cfg.SSH.EdgeAccountDeleted(ctx, m.Provider, m.Subject); err != nil {
				return fmt.Errorf("apply the deletion of %s account %s at the edge: %w", m.Provider, m.Subject, err)
			}
			if err := s.send(edgeproto.AccountDeletionApplied(m)); err != nil {
				return err
			}
		case edgeproto.Unenroll:
			if err := errors.Join(a.setEdgeState(edgeproto.StateUnclaimed), a.state.ClearOwner()); err != nil {
				return err
			}
			return errors.New("the owner removed this server at the edge; it stays reachable directly and over the tailnet")
		case edgeproto.Drain:
			return drained{m}
		default:
			return fmt.Errorf("edge sent %T on an enrolled connection", m)
		}
	}
}

// Leave tells the edge the operator removed this server, then forgets the
// owner and the claim code. The pinned edge key is kept.
func (a *Agent) Leave(ctx context.Context) error {
	c, cancel, e, err := a.enroll(ctx, a.origin, nil)
	if err != nil {
		return fmt.Errorf("edgeagent: leave %s: %w", a.origin, err)
	}
	e.pin.close()
	defer cancel()
	s := &session{c: c}
	if err := s.send(edgeproto.Unenroll{}); err != nil {
		_ = c.CloseNow()
		return fmt.Errorf("edgeagent: leave %s: %w", a.origin, err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	return errors.Join(a.setEdgeState(edgeproto.StateUnclaimed), a.state.ClearOwner(), a.state.RemoveClaimCode())
}

// maxDialMessage bounds the refusal text a DialError keeps.
const maxDialMessage = 1024

// printableText is s from another party, safe to print, log and store:
// invalid UTF-8, control and format characters (bidi overrides among
// them) and line and paragraph separators replaced, at most n bytes.
func printableText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n-len("...")], "") + "..."
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
