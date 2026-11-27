package localgw

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
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
	t.Cleanup(func() { close(unanswered) })
	connections, channels := serveControlChannels(t, server, func(method string) json.RawMessage {
		if method == protocol.MethodRunList {
			<-unanswered
		}
		return json.RawMessage(`{}`)
	}, false)
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

	// A call that may not run twice never rides a channel that may have ended.
	if _, perr := b.Call(ctx, protocol.MethodRunLaunch, nil); perr != nil {
		t.Fatal(perr)
	}
	if got := channels.Load(); got != 2 {
		t.Fatalf("a call unsafe to replay left %d control channels opened, want its own, 2", got)
	}

	// A call cancelled before its answer leaves a channel that still owes
	// that answer: it must not carry the next call.
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if _, perr := b.Call(short, protocol.MethodRunList, nil); perr == nil || perr.Message != "request cancelled" {
		t.Fatalf("unanswered call = %v, want request cancelled", perr)
	}
	before := channels.Load()
	for range 3 {
		if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
			t.Fatal(perr)
		}
	}
	if got := channels.Load() - before; got > 1 {
		t.Fatalf("3 calls after a cancelled one opened %d control channels, want at most 1", got)
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
	var arrived sync.WaitGroup
	arrived.Add(calls)
	release := make(chan struct{})
	_, channels := serveControlChannels(t, server, func(string) json.RawMessage {
		arrived.Done()
		<-release
		return json.RawMessage(`{}`)
	}, false)
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
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
	// Every call reaches the server before any is answered.
	arrived.Wait()
	close(release)
	for range calls {
		if perr := <-errs; perr != nil {
			t.Fatal(perr)
		}
	}
	if got := channels.Load(); got != calls {
		t.Fatalf("%d calls in flight together used %d control channels, want %d", calls, got, calls)
	}
}

// The server can end an idle control channel under a connection that stays
// up. The next call must be answered on a new channel, and the connection,
// which carries every stream, must survive.
func TestSSHBackendSurvivesAnIdleControlChannelEnding(t *testing.T) {
	home := testhome.Isolate(t)
	server := listen(t)
	connections, channels := serveControlChannels(t, server, nil, true)
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
