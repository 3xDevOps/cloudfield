package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/timeline"
)

// tappedPipe keeps every byte the server sent on the channel.
type tappedPipe struct {
	*subsystemPipe
	got bytes.Buffer
}

func (p *tappedPipe) Read(b []byte) (int, error) {
	n, err := p.subsystemPipe.Read(b)
	p.got.Write(b[:n])
	return n, err
}

func tappedClient(t *testing.T, e *testEnv, gzip bool) (*protocol.Client, *tappedPipe) {
	t.Helper()
	pipe := &tappedPipe{subsystemPipe: openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)}
	c := protocol.NewClient(pipe)
	if gzip {
		c.AcceptGzip()
	}
	return c, pipe
}

// A dashboard-sized timeline page and session list in the research seed's
// shapes (1,000 events; 600 sessions).
func seedLargeReads(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEnv(t, func(c *Config) {
		c.Bus = bus
		c.Services.Timeline = timeline.NewReader(log)
	})
	start := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	for i := range 600 {
		begun := start.Add(time.Duration(i) * time.Minute)
		r := &domain.Run{
			WorkspaceID: e.ws.ID, MemberID: e.member.ID,
			Task:    fmt.Sprintf("Seeded task %d: change the %s module", i, []string{"checkout", "search", "billing", "import", "report"}[i%5]),
			Title:   fmt.Sprintf("Seeded session %d", i),
			Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunCompleted,
			Branch:    fmt.Sprintf("cloudfield/seed-%05d", i),
			CreatedAt: begun, StartedAt: &begun, FinishedAt: &begun,
		}
		if err := e.db.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	for i := range 1000 {
		verified := start.Add(time.Duration(i) * time.Second)
		payload := events.Payload(events.RunStatusPayload{
			From: domain.RunRunning, To: domain.RunRunning, Activity: domain.ActivityWaitingAgents,
			StatusSeq: int64(i), Cause: events.CauseTurn, Reread: i%3 != 0,
			Wait: &domain.RunWait{Kind: domain.WaitCondition, Detail: "pull request checks", VerifiedAt: &verified},
		})
		if i%4 == 0 {
			payload = events.CoordMessagePayload{MessageID: fmt.Sprintf("msg-seed%07d", i), WorkspaceID: e.ws.ID, FromRunID: e.run.ID, ToRunID: e.run.ID, Kind: "message"}
		}
		if _, err := bus.Publish(ctx, events.Event{WorkspaceID: e.ws.ID, RunID: e.run.ID, Payload: payload}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	return e
}

// A client that asks gets the same result in fewer bytes; a client that
// does not gets exactly the JSON line a server without compression sends.
func TestControlGzipLargeReads(t *testing.T) {
	t.Parallel()
	e := seedLargeReads(t)
	reads := []struct {
		method string
		params any
	}{
		{protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{
			WorkspaceID: string(e.ws.ID), Newest: true, Limit: 1000,
			Types: []string{string(events.TypeRunStatus), string(events.TypeCoordMessage)},
		}},
		{protocol.MethodRunList, protocol.RunListParams{WorkspaceID: string(e.ws.ID)}},
	}
	for _, read := range reads {
		plain, plainPipe := tappedClient(t, e, false)
		var want json.RawMessage
		if err := plain.Call(read.method, read.params, &want); err != nil {
			t.Fatalf("%s: %v", read.method, err)
		}
		line, err := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: want})
		if err != nil {
			t.Fatal(err)
		}
		if got := plainPipe.got.Bytes(); !bytes.Equal(got, append(line, '\n')) {
			t.Fatalf("%s without accept_encoding: %d bytes on the wire, want the %d-byte JSON line", read.method, len(got), len(line)+1)
		}

		zipped, zippedPipe := tappedClient(t, e, true)
		var got json.RawMessage
		if err := zipped.Call(read.method, read.params, &got); err != nil {
			t.Fatalf("%s with gzip: %v", read.method, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s with gzip: result differs from the uncompressed one", read.method)
		}
		wire := zippedPipe.got.Len()
		if !strings.HasPrefix(zippedPipe.got.String(), "gzip ") || wire*5 > len(line) {
			t.Fatalf("%s with gzip: %d bytes on the wire for a %d-byte line, want a gzip frame under a fifth of it", read.method, wire, len(line)+1)
		}
		t.Logf("%s: %d bytes as a JSON line, %d as a gzip frame", read.method, len(line)+1, wire)
	}
}

// Only the allowlisted reads compress: a large run.get, which is not on
// the list, stays a JSON line for a client that asks.
func TestControlGzipOnlyAllowlistedResults(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: strings.Repeat("a long task ", 4<<10),
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-long",
	}
	if err := e.db.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	c, pipe := tappedClient(t, e, true)
	var got protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(run.ID)}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Run.Task != run.Task || pipe.got.Len() < protocol.GzipMinBytes || pipe.got.Bytes()[0] != '{' {
		t.Fatalf("run.get sent %d bytes starting %q, want the whole JSON line", pipe.got.Len(), pipe.got.Bytes()[:1])
	}
}
