package localgw

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/testhome"
)

// Opening a control channel costs two round trips to the server before the
// call's own, so calls one after another must share one channel.
func TestSSHBackendReusesAnAnsweredControlChannel(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	unanswered := make(chan struct{})
	connections, channels := serveControlChannels(t, server, func(method string) json.RawMessage {
		if method == protocol.MethodRunList {
			<-unanswered
		}
		return json.RawMessage(`{}`)
	}, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for range 5 {
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
	}
	if got := channels.Load(); got != 1 {
		t.Fatalf("5 calls in a row opened %d control channels, want 1", got)
	}

	// Only reads ride a kept channel: a write opens its own, whether or
	// not it may run twice.
	for i, method := range []string{protocol.MethodRunLaunch, protocol.MethodWatchDelete} {
		if _, perr := b.Call(ctx, method, nil); perr != nil {
			t.Fatal(perr)
		}
		if got := channels.Load(); got != int32(2+i) {
			t.Fatalf("%s left %d control channels opened, want its own, %d", method, got, 2+i)
		}
	}

	// A call cancelled before its answer leaves a channel that still owes
	// that answer: it must not carry the next call.
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if _, perr := b.Call(short, protocol.MethodRunList, nil); perr == nil || perr.Message != "request cancelled" {
		t.Fatalf("unanswered call = %v, want request cancelled", perr)
	}
	// Three channels were kept and the cancelled call took one.
	backend := b.(*sshBackend)
	backend.mu.Lock()
	kept := len(backend.idle)
	backend.mu.Unlock()
	if kept != 2 {
		t.Fatalf("%d control channels kept after a cancelled call, want 2: its own must be dropped", kept)
	}
	close(unanswered)
	for range 3 {
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1", got)
	}
}

// One channel carries one call at a time: calls in flight together each get
// their own, and none waits for another's answer.
func TestSSHBackendGivesConcurrentCallsTheirOwnChannels(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	const calls = 4
	arrived := make(chan struct{}, calls)
	release := make(chan struct{})
	_, channels := serveControlChannels(t, server, func(string) json.RawMessage {
		arrived <- struct{}{}
		<-release
		return json.RawMessage(`{}`)
	}, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// One kept channel, so the calls contend for it.
	go func() { <-arrived; release <- struct{}{} }()
	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	errs := make(chan *protocol.Error, calls)
	for range calls {
		go func() {
			_, perr := b.Call(ctx, protocol.MethodServerInfo, nil)
			errs <- perr
		}()
	}
	// Every call reaches the server before any is answered.
	for range calls {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("a call waited for another's answer")
		}
	}
	close(release)
	for range calls {
		if perr := <-errs; perr != nil {
			t.Fatal(perr)
		}
	}
	if got := channels.Load(); got != calls {
		t.Fatalf("%d calls in flight together, one channel kept before them, used %d control channels, want %d", calls, got, calls)
	}
}

// The server can end an idle control channel under a connection that stays
// up. The next call must be answered on a new channel, and the connection,
// which carries every stream, must survive.
func TestSSHBackendSurvivesAnIdleControlChannelEnding(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	connections, channels := serveControlChannels(t, server, nil, true, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for range 3 {
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want the first kept, 1", got)
	}
	if got := channels.Load(); got != 3 {
		t.Fatalf("3 calls on channels that end after one answer opened %d, want 3", got)
	}
}

func shortenKeptFresh(t *testing.T, d time.Duration) {
	old := keptFresh
	keptFresh = d
	t.Cleanup(func() { keptFresh = old })
}

// A kept channel skips the channel open that proves the connection answers
// before a call's watchdog starts, so it carries a call only while the
// server has just been heard from.
func TestSSHBackendOpensAChannelOnAQuietConnection(t *testing.T) {
	shortenKeptFresh(t, 200*time.Millisecond)
	home := testhome.Isolate(t)
	server := listen(t)
	_, channels := serveControlChannels(t, server, nil, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := func() {
		t.Helper()
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
	}
	call()
	time.Sleep(400 * time.Millisecond)
	call()
	if got := channels.Load(); got != 2 {
		t.Fatalf("a call on a connection quiet for longer than keptFresh left %d control channels opened, want its own, 2", got)
	}
}

// On a link that went silent the watchdog fires long before the keepalive
// drops the connection. A call made then must wait for the drop and be
// answered over a new connection, as it was when every call opened a channel.
func TestSSHBackendRedialsASilentConnectionWithKeptChannels(t *testing.T) {
	shortenKeptFresh(t, 100*time.Millisecond)
	// As in production, the keepalive drop (10 intervals) outlasts the watchdog.
	shortenKeepalive(t, 100*time.Millisecond, 200*time.Millisecond)
	home := testhome.Isolate(t)
	server := listen(t)
	connections := serveControl(t, server, nil)
	path := newSilencer(t, server.Addr().String(), 0)
	b := NewSSHBackend(cli.Config{Addr: path.addr(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	path.silence()
	time.Sleep(200 * time.Millisecond)
	// Shortened only now, so a slow machine cannot time out the healthy call.
	old := callTimeout
	callTimeout = 300 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatalf("call on a silent connection that holds a kept channel = %v, want it answered over a new one", perr)
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("server accepted %d connections, want 2", got)
	}
}

// A call is sent at most twice, as before channels were kept: once on a
// kept channel and once more on a new one. When both fail on transport the
// error is final, with no third send after a redial.
func TestSSHBackendSendsACallAtMostTwice(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	var sends atomic.Int32
	_, _ = serveControlChannels(t, server, func(method string) json.RawMessage {
		if method == protocol.MethodRunList {
			sends.Add(1)
			return nil
		}
		return json.RawMessage(`{}`)
	}, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	_, perr := b.Call(ctx, protocol.MethodRunList, nil)
	if perr == nil || perr.Code != protocol.CodeUnavailable || !strings.HasPrefix(perr.Message, "server unreachable: ") {
		t.Fatalf("call the server never answers = %v, want server unreachable", perr)
	}
	if got := sends.Load(); got != 2 {
		t.Fatalf("the call reached the server %d times, want 2", got)
	}
}

// The replay after a redial is a call's second send. It must open its own
// channel: a kept one that has ended would ask for a third.
func TestSSHBackendReplayTakesNoKeptChannel(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	_, channels := serveControlChannels(t, server, nil, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")}).(*sshBackend)
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	if _, err := b.callOnce(ctx, protocol.MethodServerInfo, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := channels.Load(); got != 2 {
		t.Fatalf("a replay beside a kept channel left %d control channels opened, want its own, 2", got)
	}
}

// A caller that gave up while its call was on a kept channel gets "request
// cancelled", and the call is not written to a new channel for nobody.
func TestSSHBackendCancelledCallOnAKeptChannelIsNotSentAgain(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	_, channels := serveControlChannels(t, server, nil, true, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The channel this call keeps has ended on the server's side.
	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	gone, stop := context.WithCancel(ctx)
	stop()
	if _, perr := b.Call(gone, protocol.MethodServerInfo, nil); perr == nil || perr.Message != "request cancelled" {
		t.Fatalf("call for a caller that gave up = %v, want request cancelled", perr)
	}
	if got := channels.Load(); got != 1 {
		t.Fatalf("a cancelled call left %d control channels opened, want no new one, 1", got)
	}
}

// Kept channels belong to their connection: a relink or a dropped
// connection must leave none behind for the next connection's calls.
func TestSSHBackendDropsKeptChannelsWithTheirConnection(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	serveControl(t, server, nil)
	cfg := cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")}
	b := NewSSHBackend(cfg).(*sshBackend)
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kept := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.idle)
	}
	call := func() {
		t.Helper()
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
		if got := kept(); got != 1 {
			t.Fatalf("%d control channels kept after an answered call, want 1", got)
		}
	}
	call()
	next, err := dialLink(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.Relink(cfg, next)
	if got := kept(); got != 0 {
		t.Fatalf("%d control channels kept across a relink, want 0", got)
	}
	call()
	b.invalidate(next)
	if got := kept(); got != 0 {
		t.Fatalf("%d control channels kept after their connection was dropped, want 0", got)
	}
}

// A burst wider than maxIdleControls leaves only that many channels open
// on the server afterwards.
func TestSSHBackendKeepsAtMostMaxIdleControls(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	const calls = maxIdleControls + 2
	arrived := make(chan struct{}, calls)
	release := make(chan struct{})
	serveControl(t, server, func(string) json.RawMessage {
		arrived <- struct{}{}
		<-release
		return json.RawMessage(`{}`)
	})
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")}).(*sshBackend)
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errs := make(chan *protocol.Error, calls)
	for range calls {
		go func() {
			_, perr := b.Call(ctx, protocol.MethodServerInfo, nil)
			errs <- perr
		}()
	}
	for range calls {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("the burst did not reach the server")
		}
	}
	close(release)
	for range calls {
		if perr := <-errs; perr != nil {
			t.Fatal(perr)
		}
	}
	b.mu.Lock()
	kept := len(b.idle)
	b.mu.Unlock()
	if kept != maxIdleControls {
		t.Fatalf("%d control channels kept after %d calls at once, want %d", kept, calls, maxIdleControls)
	}
}

// The gateway asks every server for compressed answers; one that does not
// know the field answers with plain JSON lines, which the gateway reads as
// before, on a new channel and on a kept one.
func TestSSHBackendReadsPlainAnswersFromAServerWithoutCompression(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	big, err := json.Marshal(map[string]string{"text": strings.Repeat("plain answer ", 64<<10)})
	if err != nil {
		t.Fatal(err)
	}
	serveControlChannels(t, server, func(string) json.RawMessage { return big }, false, nil)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 2 {
		got, perr := b.Call(ctx, protocol.MethodRunList, nil)
		if perr != nil {
			t.Fatal(perr)
		}
		if string(got) != string(big) {
			t.Fatalf("run.list returned %d bytes, want the server's %d", len(got), len(big))
		}
	}
}

// The gateway asks for compression only on the methods the server may
// compress, and reads the frames a compressing server sends, on a new
// channel and on a kept one, and a plain answer on the same kept channel
// after them. Every other request is byte for byte what it was before.
func TestSSHBackendReadsCompressedAnswers(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	big, err := json.Marshal(map[string]string{"text": strings.Repeat("compressed answer ", 64<<10)})
	if err != nil {
		t.Fatal(err)
	}
	small := json.RawMessage(`{"small":true}`)
	gz := new(compressingServer)
	_, channels := serveControlChannels(t, server, func(method string) json.RawMessage {
		if method == protocol.MethodWorkspaceTimeline {
			return big
		}
		return small
	}, false, gz)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, call := range []struct {
		method string
		want   json.RawMessage
	}{
		{protocol.MethodWorkspaceTimeline, big},
		{protocol.MethodWorkspaceTimeline, big},
		{protocol.MethodServerInfo, small},
	} {
		got, perr := b.Call(ctx, call.method, nil)
		if perr != nil {
			t.Fatalf("call %d, %s: %v", i, call.method, perr)
		}
		if string(got) != string(call.want) {
			t.Fatalf("call %d, %s: %d bytes, want the server's %d", i, call.method, len(got), len(call.want))
		}
	}
	if got := gz.frames.Load(); got != 2 {
		t.Fatalf("server sent %d gzip frames, want one per timeline call", got)
	}
	if got := channels.Load(); got != 1 {
		t.Fatalf("3 calls opened %d control channels, want 1 kept channel", got)
	}

	launch := json.RawMessage(`{"workspace_id":"ws-1","task":"fix the build"}`)
	if _, perr := b.Call(ctx, protocol.MethodRunLaunch, launch); perr != nil {
		t.Fatal(perr)
	}
	lines := gz.requestLines()
	if len(lines) != 4 || !strings.Contains(lines[0], `"accept_encoding":"gzip"`) || strings.Contains(lines[2], "accept_encoding") {
		t.Fatalf("requests = %q, want accept_encoding on the timeline calls only", lines)
	}
	today, err := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodRunLaunch, Params: launch})
	if err != nil {
		t.Fatal(err)
	}
	if lines[3] != string(today) {
		t.Fatalf("run.launch request line = %s, want today's %s", lines[3], today)
	}
}
