package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/webgate"
)

// sshBackend proxies the Backend surface onto one lazily dialed SSH connection.
// Each stream opens its own channel; calls share control channels, one call
// at a time on each. mu guards the fields and is
// never held across network I/O: a dial through an edge can take most of a
// minute, and every request, Relink and Close would queue behind it.
type sshBackend struct {
	mu   sync.Mutex
	cfg  cli.Config
	conn *cli.Conn
	// idle holds conn's control channels whose last call got its answer.
	// Opening one costs two round trips to the server before the call's own.
	idle []*protocol.Client
	// dialing is the dial every caller without a connection waits on.
	dialing *dialAttempt
	closed  bool
	// silent is the last connection the keepalive dropped, so a call that
	// failed on it says why.
	silent *cli.Conn
	// The keepalive's own copy, taken at construction, so a goroutine of
	// a closed backend never reads a setting a test changes.
	keepaliveInterval time.Duration
	keepaliveTimeout  time.Duration
}

// dialAttempt is one dial of cfg; conn and err are set before done closes.
type dialAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	cfg    cli.Config
	conn   *cli.Conn
	err    error
}

// dialTimeout bounds one dial, a link's server.info call included. The
// edge path's own steps add up to under a minute; a plain address's
// handshake has no deadline of its own.
const dialTimeout = 90 * time.Second

// keepaliveInterval bounds how long a connection whose network went away
// without a reset stays cached: a phone that slept or changed networks
// leaves one, and every call and stream opened on it waits for an answer
// that never comes. The connection is dropped once keepaliveCount
// intervals pass without a byte from the server; a keepalive is sent only
// after an interval without one, so an idle phone's radio can sleep.
// keepaliveTimeout bounds the probe of a connection a stream failed to
// open on. Both leave a server stalled by memory pressure on a saturated
// host time to answer: tearing down a live connection ends every stream.
// Variables so a test can shorten them.
var (
	keepaliveInterval = 30 * time.Second
	keepaliveTimeout  = 60 * time.Second
)

const keepaliveCount = 10

var (
	errClosed   = errors.New("the gateway is closing")
	errRelinked = errors.New("the gateway moved to another server while connecting")
	errSilent   = errors.New("the server stopped answering keepalives")
)

// NewSSHBackend returns a Backend that redials once when the connection drops
// under a replay-safe call.
func NewSSHBackend(cfg cli.Config) Backend {
	return &sshBackend{cfg: cfg, keepaliveInterval: keepaliveInterval, keepaliveTimeout: keepaliveTimeout}
}

func (b *sshBackend) Close() error {
	b.mu.Lock()
	b.closed = true
	conn := b.swap(nil)
	b.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// Relink closes the previous connection before it returns, so a stream
// blocked on it ends.
func (b *sshBackend) Relink(cfg cli.Config, conn *cli.Conn) {
	b.mu.Lock()
	b.cfg = cfg
	old := b.swap(conn)
	b.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// swap caches conn, abandoning any dial in progress, and returns the
// connection it replaced. The caller holds mu.
func (b *sshBackend) swap(conn *cli.Conn) *cli.Conn {
	if b.dialing != nil {
		b.dialing.cancel()
		b.dialing = nil
	}
	old := b.conn
	b.conn = conn
	b.idle = nil
	if conn != nil {
		go b.keepalive(conn)
	}
	return old
}

// live returns the shared connection and the link it was dialed for,
// dialing if needed. Concurrent callers share one dial, and each stops
// waiting when its own ctx ends. Dial errors come back classified so every
// surface reports them identically. A dial that Relink abandoned sends its
// callers back for the new config.
func (b *sshBackend) live(ctx context.Context) (*cli.Conn, cli.Config, error) {
	for {
		b.mu.Lock()
		if b.conn != nil {
			conn, cfg := b.conn, b.cfg
			b.mu.Unlock()
			return conn, cfg, nil
		}
		if b.closed {
			b.mu.Unlock()
			return nil, cli.Config{}, unreachableError(errClosed)
		}
		d := b.dialing
		if d == nil {
			dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			d = &dialAttempt{done: make(chan struct{}), cancel: cancel, cfg: b.cfg}
			b.dialing = d
			go b.dial(dialCtx, d)
		}
		b.mu.Unlock()
		select {
		case <-d.done:
			if errors.Is(d.err, errRelinked) {
				continue
			}
			if d.err != nil {
				return nil, cli.Config{}, unreachableError(d.err)
			}
			return d.conn, d.cfg, nil
		case <-ctx.Done():
			return nil, cli.Config{}, &protocol.Error{Code: protocol.CodeUnavailable, Message: "request cancelled"}
		}
	}
}

func (b *sshBackend) dial(ctx context.Context, d *dialAttempt) {
	defer d.cancel()
	conn, err := dialLink(ctx, d.cfg)
	var stale *cli.Conn
	b.mu.Lock()
	if b.dialing == d {
		b.dialing = nil
		if err == nil {
			b.swap(conn)
		}
	} else {
		// Relink or Close abandoned this dial: its config is no longer
		// the one served.
		stale, conn, err = conn, nil, errRelinked
		if b.closed {
			err = errClosed
		}
	}
	d.conn, d.err = conn, err
	b.mu.Unlock()
	close(d.done)
	if stale != nil {
		_ = stale.Close()
	}
}

// keepalive watches conn while it is the cached connection and drops it
// once the server has been silent too long, which ends every call and
// stream waiting on it. The reply to a keepalive is the only traffic an
// idle connection gets.
func (b *sshBackend) keepalive(conn *cli.Conn) {
	tick := time.NewTicker(b.keepaliveInterval)
	defer tick.Stop()
	for range tick.C {
		b.mu.Lock()
		cached := b.conn == conn
		b.mu.Unlock()
		if !cached {
			return
		}
		idle := time.Since(conn.LastRead())
		if idle >= keepaliveCount*b.keepaliveInterval {
			b.mu.Lock()
			b.silent = conn
			b.mu.Unlock()
			b.invalidate(conn)
			return
		}
		if idle >= b.keepaliveInterval {
			go func() { _ = conn.Ping(context.Background()) }()
		}
	}
}

// invalidate closes conn only if it is still the cached one: a replaced
// connection may carry other live streams.
func (b *sshBackend) invalidate(conn *cli.Conn) {
	b.mu.Lock()
	cached := b.conn == conn
	if cached {
		b.conn = nil
		b.idle = nil
	}
	b.mu.Unlock()
	if cached {
		_ = conn.Close()
	}
}

// endToEnd marks err, a failure on a connection already open to the
// server of cfg, as the end-to-end layer of the edge path when cfg
// reaches that server through an edge only. A link with an address too
// may be connected either way, so its failure names no edge.
func endToEnd(cfg cli.Config, err error) error {
	if cfg.ServerID == "" || cfg.EdgeURL == "" || cfg.Addr != "" {
		return err
	}
	return &cli.EdgePathError{Layer: cli.LayerEndToEnd, ServerID: cfg.ServerID, Edge: edgeclient.HostOf(cfg.EdgeURL), Err: err}
}

// unreachableError classifies a transport failure for the SPA, which routes on
// the message prefix. Only unambiguous local failures (DNS, no route, interface
// down) say "network unreachable"; a refused connection or a timeout could be
// either side, so it stays "server unreachable".
func unreachableError(err error) *protocol.Error {
	prefix := "server unreachable: "
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.ENETDOWN) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) {
		prefix = "network unreachable: "
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: prefix + err.Error(), Data: refusalData(err)}
}

// callTimeout bounds one control call. Only that call fails when it passes:
// a slow server handler is not a dead connection, and the keepalive drops a
// dead one. A variable so a test can shorten it.
var callTimeout = 60 * time.Second

// transferTimeout is the server's lifetime for one large control frame: a
// 64 MiB file does not cross a slow link within callTimeout, nor does
// workspace.import's first fetch of a large repository.
const transferTimeout = 15 * time.Minute

func callBound(method string) time.Duration {
	switch method {
	case protocol.MethodAgentInstall:
		return protocol.AgentInstallTimeout + callTimeout
	case protocol.MethodEnvSave:
		return protocol.EnvSaveCheckTimeout + callTimeout
	case protocol.MethodAssistantStart, protocol.MethodAssistantPrompt:
		return protocol.AssistantStartTimeout + callTimeout
	case protocol.MethodHomeUpload, protocol.MethodHomeDownload, protocol.MethodTerminalFile, protocol.MethodWorkspaceImport, protocol.MethodFilesUpload:
		return transferTimeout
	case protocol.MethodTerminalExec:
		return protocol.TerminalExecTimeout + callTimeout
	case protocol.MethodDictationTranscribe:
		return protocol.DictationTimeout + callTimeout
	}
	return callTimeout
}

// maxIdleControls is how many answered control channels a connection keeps
// for its next calls: the browser sends at most six requests at a time.
const maxIdleControls = 8

// keptFresh is how recently the server must have sent a byte for a call to
// go on a kept channel. Opening a channel proves the connection answers
// before the call's watchdog starts; a kept channel has only this, so a
// link that dies inside the window fails the call at its watchdog. The
// window covers calls that follow an answer, as a page's reads do. On a
// quieter connection the call opens its own channel, which waits out a
// dead link until the keepalive drops it and the call is redialed. A
// variable so a test can shorten it.
var keptFresh = time.Second

// idleControl takes one of conn's answered control channels, nil when it
// has none or conn has been quiet.
func (b *sshBackend) idleControl(conn *cli.Conn) *protocol.Client {
	if time.Since(conn.LastRead()) > keptFresh {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != conn || len(b.idle) == 0 {
		return nil
	}
	client := b.idle[len(b.idle)-1]
	b.idle = b.idle[:len(b.idle)-1]
	return client
}

// keepControl hands back a channel whose call was answered, closing it when
// conn is no longer the cached connection or already holds enough.
func (b *sshBackend) keepControl(conn *cli.Conn, client *protocol.Client) {
	b.mu.Lock()
	keep := b.conn == conn && len(b.idle) < maxIdleControls
	if keep {
		b.idle = append(b.idle, client)
	}
	b.mu.Unlock()
	if !keep {
		_ = client.Close()
	}
}

// roundTrip runs one call alone on its channel. Cancellation and the watchdog
// close the channel, to unblock the pending read. Their errors are
// coded so Call does not repeat a call the server may still be running.
// answered reports that the server's response was read, which leaves the
// channel open and ready for another call; otherwise it is closed.
func roundTrip(ctx context.Context, client *protocol.Client, method string, params json.RawMessage) (result json.RawMessage, answered bool, err error) {
	var callParams any
	if len(params) > 0 {
		callParams = params
	}
	type outcome struct {
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		var result json.RawMessage
		err := client.Call(method, callParams, &result)
		done <- outcome{result: result, err: err}
	}()
	bound := callBound(method)
	watchdog := time.NewTimer(bound)
	defer watchdog.Stop()
	select {
	case out := <-done:
		var refused *protocol.Error
		if out.err == nil || errors.As(out.err, &refused) {
			return out.result, true, out.err
		}
		_ = client.Close()
		return nil, false, out.err
	case <-ctx.Done():
		_ = client.Close()
		return nil, false, &protocol.Error{Code: protocol.CodeUnavailable, Message: "request cancelled"}
	case <-watchdog.C:
		_ = client.Close()
		return nil, false, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf("server did not answer %s within %v", method, bound)}
	}
}

// callOnce drops the connection on transport failures so the next attempt
// redials; server refusals, cancellations and timeouts leave it alone.
// With reuse the call goes on an idle control channel when there is one. An
// idle channel can have ended under a live connection, so a transport
// failure on one is sent again on a new channel before it counts against
// the connection. That second send is the call's one replay: when it fails
// too, the error is final and Call does not send a third.
func (b *sshBackend) callOnce(ctx context.Context, method string, params json.RawMessage, reuse bool) (json.RawMessage, error) {
	conn, cfg, err := b.live(ctx)
	if err != nil {
		return nil, err
	}
	var perr *protocol.Error
	resent := false
	if reuse {
		if client := b.idleControl(conn); client != nil {
			resent = true
			result, answered, cerr := roundTrip(ctx, client, method, params)
			if answered {
				b.keepControl(conn, client)
			}
			if answered || errors.As(cerr, &perr) {
				return result, cerr
			}
			if ctx.Err() != nil {
				return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "request cancelled"}
			}
		}
	}
	client, err := conn.Control()
	if err != nil {
		b.invalidate(conn)
		return nil, endToEnd(cfg, err)
	}
	result, answered, err := roundTrip(ctx, client, method, params)
	if answered {
		b.keepControl(conn, client)
	}
	if err == nil {
		return result, nil
	}
	if errors.As(err, &perr) {
		return nil, err
	}
	b.invalidate(conn)
	b.mu.Lock()
	silent := b.silent == conn
	b.mu.Unlock()
	if silent {
		err = fmt.Errorf("%w: %w", errSilent, err)
	}
	if resent {
		return nil, unreachableError(endToEnd(cfg, err))
	}
	return nil, endToEnd(cfg, err)
}

// keptReads holds the methods that may ride a kept channel: reads the
// dashboard makes as a page loads, which change nothing on the server. On
// a link that dies or a server that stalls, a call on a kept channel fails
// at its watchdog with its outcome unknown, where one that opens a channel
// waits or is redialed; a write must keep the second behaviour.
var keptReads = map[string]bool{
	protocol.MethodServerInfo:         true,
	protocol.MethodWorkspaceList:      true,
	protocol.MethodMemberList:         true,
	protocol.MethodRunList:            true,
	protocol.MethodRunOverlaps:        true,
	protocol.MethodMemberDeviceList:   true,
	protocol.MethodWatchList:          true,
	protocol.MethodMemberMergePending: true,
	protocol.MethodServerEdge:         true,
	protocol.MethodLinearStatus:       true,
	protocol.MethodAgentList:          true,
	protocol.MethodBudgetGet:          true,
	protocol.MethodPresenceRoster:     true,
	protocol.MethodLaunchDefaultsGet:  true,
	protocol.MethodAssistantStatus:    true,
	protocol.MethodServerSysboxStatus: true,
	protocol.MethodWorkspaceTimeline:  true,
	protocol.MethodRunGet:             true,
	protocol.MethodRunChildren:        true,
	protocol.MethodCoordMessagesList:  true,
	protocol.MethodRunRoomList:        true,
	protocol.MethodRunRoomStatus:      true,
	protocol.MethodRunACPHistory:      true,
	protocol.MethodChangesList:        true,
}

// noReplay holds the methods unsafe to run twice: a second run creates a
// duplicate, repeats an external side effect, or answers with an error such
// as not found where the first succeeded. Prefixes in Call cover dev.*,
// run.git.* and run.pr.*.
var noReplay = map[string]bool{
	protocol.MethodRunLaunch:       true,
	protocol.MethodRunDelete:       true,
	protocol.MethodRunPause:        true,
	protocol.MethodRunResume:       true,
	protocol.MethodRunRelaunch:     true,
	protocol.MethodRunHandoff:      true,
	protocol.MethodRunModeSwitch:   true,
	protocol.MethodRunInputAnswer:  true,
	protocol.MethodRunInputRespond: true,
	protocol.MethodRunACPCancel:    true,
	protocol.MethodRunInterrupt:    true,
	protocol.MethodRunOptionSet:    true,
	protocol.MethodTemplateLaunch:  true,
	protocol.MethodTemplateDelete:  true,
	protocol.MethodScheduleDelete:  true,

	protocol.MethodDictationTranscribe: true,

	protocol.MethodPreferenceAdd:    true,
	protocol.MethodPreferenceUpdate: true,
	protocol.MethodPreferenceDelete: true,

	protocol.MethodAgentLoginAdd:       true,
	protocol.MethodAgentLoginRename:    true,
	protocol.MethodAgentLoginRemove:    true,
	protocol.MethodAgentLoginOrder:     true,
	protocol.MethodAgentLoginAutomatic: true,
	protocol.MethodAgentLoginShare:     true,

	protocol.MethodWorkspaceAdd:             true,
	protocol.MethodWorkspaceDelete:          true,
	protocol.MethodWorkspaceImport:          true,
	protocol.MethodWorkspaceMirrorConfigure: true,
	protocol.MethodWorkspaceMirrorRefresh:   true,
	protocol.MethodWorkspaceMirrorAdopt:     true,
	protocol.MethodWorkspaceMirrorDisable:   true,

	protocol.MethodMemberInvite:           true,
	protocol.MethodMemberRemove:           true,
	protocol.MethodMemberDeviceApprove:    true,
	protocol.MethodMemberDeviceRevoke:     true,
	protocol.MethodMemberInvitationCreate: true,
	protocol.MethodMemberInvitationRevoke: true,
	protocol.MethodMemberIdentityLink:     true,
	protocol.MethodMemberIdentityRemove:   true,
	protocol.MethodMemberReviewerLink:     true,
	protocol.MethodMemberMergeStart:       true,
	protocol.MethodMemberMergePropose:     true,
	protocol.MethodMemberMergeAccept:      true,
	protocol.MethodConnectorCode:          true,
	protocol.MethodConnectorRevoke:        true,
	protocol.MethodSlackLink:              true,
	protocol.MethodSlackUnlink:            true,

	protocol.MethodServerUpdate:        true,
	protocol.MethodServerOwnerTransfer: true,
	protocol.MethodServerSysboxInstall: true,

	protocol.MethodHomeWrite:    true,
	protocol.MethodHomeMkdir:    true,
	protocol.MethodHomeRename:   true,
	protocol.MethodHomeRemove:   true,
	protocol.MethodHomeUpload:   true,
	protocol.MethodConfigWrite:  true,
	protocol.MethodConfigImport: true,
	protocol.MethodFilesWrite:   true,
	protocol.MethodFilesUpload:  true,

	protocol.MethodTerminalExec:  true,
	protocol.MethodTerminalImage: true,
	protocol.MethodTerminalFile:  true,
	protocol.MethodEnvSave:       true,

	protocol.MethodAssistantStart:       true,
	protocol.MethodAssistantPrompt:      true,
	protocol.MethodAssistantAnswer:      true,
	protocol.MethodAssistantConfirm:     true,
	protocol.MethodAssistantQueueRemove: true,
	protocol.MethodAssistantReset:       true,
	protocol.MethodAssistantRuleRevoke:  true,

	protocol.MethodPushUnsubscribe:   true,
	protocol.MethodPushTest:          true,
	protocol.MethodGitHubConnect:     true,
	protocol.MethodGitHubOAuthStart:  true,
	protocol.MethodGitHubOAuthCancel: true,
	protocol.MethodAgentInstall:      true,
	protocol.MethodAgentRemove:       true,
	protocol.MethodProfilePush:       true,
	protocol.MethodSyncConflict:      true,
}

// Call redials and retries once on a transport failure, except for methods
// that may have committed a mutation before the response was lost.
func (b *sshBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	replaySafe := !noReplay[method] && !strings.HasPrefix(method, "dev.") && !strings.HasPrefix(method, "run.git.") && !strings.HasPrefix(method, "run.pr.")
	result, err := b.callOnce(ctx, method, params, replaySafe && keptReads[method])
	if err == nil {
		return result, nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return nil, perr
	}
	if !replaySafe {
		return nil, unreachableError(err)
	}
	// The replay is the second send, so it takes no kept channel that could ask for a third.
	result, err = b.callOnce(ctx, method, params, false)
	if err == nil {
		return result, nil
	}
	if errors.As(err, &perr) {
		return nil, perr
	}
	return nil, unreachableError(err)
}

// alive tells a refusal on a healthy connection from a dead one; tearing down
// a healthy connection would kill every stream riding on it.
func (b *sshBackend) alive(conn *cli.Conn) bool {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), b.keepaliveTimeout)
	defer cancel()
	return conn.Ping(ctx) == nil || conn.LastRead().After(start)
}

// stream opens one subsystem channel. Errors the server answered (attach/sync
// ack refusals are untyped, hence the keepalive probe) and any error once ctx
// ends pass through; only a dead connection is invalidated, redialed and
// retried once.
func stream[T any](ctx context.Context, b *sshBackend, open func(*cli.Conn) (T, error)) (T, error) {
	conn, _, err := b.live(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	out, oerr := open(conn)
	var perr *protocol.Error
	if oerr == nil || errors.As(oerr, &perr) || ctx.Err() != nil || b.alive(conn) {
		return out, oerr
	}
	b.invalidate(conn)
	conn, _, err = b.live(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return open(conn)
}

func (b *sshBackend) Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	return stream(ctx, b, func(c *cli.Conn) (io.ReadCloser, error) { return c.EventsStream(req) })
}

func (b *sshBackend) Attach(ctx context.Context, req protocol.AttachRequest) (webgate.Terminal, protocol.AttachResponse, error) {
	type attachResult struct {
		term webgate.Terminal
		ack  protocol.AttachResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (attachResult, error) {
		term, ack, err := c.AttachStream(req)
		if term == nil {
			return attachResult{ack: ack}, err
		}
		return attachResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) ACP(ctx context.Context, req protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
	type acpResult struct {
		stream io.ReadWriteCloser
		ack    protocol.ACPStreamResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (acpResult, error) {
		s, ack, err := c.ACPStream(req)
		if s == nil {
			return acpResult{ack: ack}, err
		}
		return acpResult{stream: s, ack: ack}, err
	})
	return out.stream, out.ack, err
}

func (b *sshBackend) Assistant(ctx context.Context, req protocol.AssistantStreamRequest) (io.ReadCloser, protocol.ACPStreamResponse, error) {
	type assistantResult struct {
		stream io.ReadCloser
		ack    protocol.ACPStreamResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (assistantResult, error) {
		s, ack, err := c.AssistantStream(req)
		if s == nil {
			return assistantResult{ack: ack}, err
		}
		return assistantResult{stream: s, ack: ack}, err
	})
	return out.stream, out.ack, err
}

func (b *sshBackend) Terminal(ctx context.Context, req protocol.TerminalRequest) (webgate.Terminal, protocol.TerminalResponse, error) {
	type terminalResult struct {
		term webgate.Terminal
		ack  protocol.TerminalResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (terminalResult, error) {
		term, ack, err := c.TerminalStream(req)
		if term == nil {
			return terminalResult{ack: ack}, err
		}
		return terminalResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	return stream(context.Background(), b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.Sync(runID, force) })
}

func (b *sshBackend) Forward(target string, port uint32) (io.ReadWriteCloser, error) {
	return stream(context.Background(), b, func(c *cli.Conn) (io.ReadWriteCloser, error) {
		return c.Forward(target, port)
	})
}

func (b *sshBackend) BrowserFrames(ctx context.Context, req protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	return stream(ctx, b, func(c *cli.Conn) (io.ReadCloser, error) { return c.BrowserFrames(ctx, req) })
}

func (b *sshBackend) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	type capture struct {
		stream   io.ReadCloser
		artifact protocol.DevArtifact
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (capture, error) {
		source, artifact, err := c.Artifact(ctx, req)
		return capture{stream: source, artifact: artifact}, err
	})
	return out.stream, out.artifact, err
}

var (
	_ webgate.DevelopmentBackend = (*sshBackend)(nil)
	_ webgate.ACPBackend         = (*sshBackend)(nil)
	_ webgate.AssistantBackend   = (*sshBackend)(nil)
)
