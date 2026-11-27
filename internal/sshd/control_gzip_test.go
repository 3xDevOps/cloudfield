package sshd

import (
	"bufio"
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
	e.bus = bus
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

// A client that asks gets the same timeline page in fewer bytes; a client
// that does not gets exactly the JSON line a server without compression
// sends.
func TestControlGzipTimelinePage(t *testing.T) {
	t.Parallel()
	e := seedLargeReads(t)
	params := protocol.WorkspaceTimelineParams{
		WorkspaceID: string(e.ws.ID), Newest: true, Limit: 1000,
		Types: []string{string(events.TypeRunStatus), string(events.TypeCoordMessage)},
	}
	plain, plainPipe := tappedClient(t, e, false)
	var want json.RawMessage
	if err := plain.Call(protocol.MethodWorkspaceTimeline, params, &want); err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: want})
	if err != nil {
		t.Fatal(err)
	}
	if got := plainPipe.got.Bytes(); !bytes.Equal(got, append(line, '\n')) {
		t.Fatalf("without accept_encoding: %d bytes on the wire, want the %d-byte JSON line", len(got), len(line)+1)
	}

	zipped, zippedPipe := tappedClient(t, e, true)
	var got json.RawMessage
	if err := zipped.Call(protocol.MethodWorkspaceTimeline, params, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("with gzip: result differs from the uncompressed one")
	}
	wire := zippedPipe.got.Len()
	if !strings.HasPrefix(zippedPipe.got.String(), "gzip ") || wire*5 > len(line) {
		t.Fatalf("with gzip: %d bytes on the wire for a %d-byte line, want a gzip frame under a fifth of it", wire, len(line)+1)
	}
	t.Logf("workspace.timeline: %d bytes as a JSON line, %d as a gzip frame", len(line)+1, wire)
}

// Only workspace.timeline compresses. run.list and changes.list can span
// workspaces in one response, and run.get is not on the list: each stays a
// JSON line for a client that asks, however large.
func TestControlGzipOnlyTheTimeline(t *testing.T) {
	t.Parallel()
	e := seedLargeReads(t)
	run := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: strings.Repeat("a long task ", 4<<10),
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-long",
	}
	if err := e.db.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	c, pipe := tappedClient(t, e, true)
	for _, read := range []struct {
		method string
		params any
	}{
		{protocol.MethodRunList, protocol.RunListParams{}},
		{protocol.MethodChangesList, protocol.ChangesListParams{Limit: 1000}},
		{protocol.MethodRunGet, protocol.RunIDParams{RunID: string(run.ID)}},
	} {
		pipe.got.Reset()
		if err := c.Call(read.method, read.params, nil); err != nil {
			t.Fatalf("%s: %v", read.method, err)
		}
		if pipe.got.Len() < protocol.GzipMinBytes || pipe.got.Bytes()[0] != '{' {
			t.Fatalf("%s sent %d bytes starting %q, want a JSON line of at least %d bytes", read.method, pipe.got.Len(), pipe.got.Bytes()[:1], protocol.GzipMinBytes)
		}
	}
}

// A timeline page holding a payload only some readers of the workspace may
// see, here an assistant notice addressed to the caller, stays a JSON line;
// the same page without it compresses.
func TestControlGzipSendsMixedTimelinePagesPlain(t *testing.T) {
	t.Parallel()
	e := seedLargeReads(t)
	if _, err := e.bus.Publish(context.Background(), events.Event{
		WorkspaceID: e.ws.ID, RunID: e.run.ID,
		Payload: events.AssistantNoticePayload{MemberID: e.member.ID, RunID: e.run.ID, Kind: events.AssistantNoticeFinished, Title: "done", Body: "the private notice"},
	}); err != nil {
		t.Fatal(err)
	}
	c, pipe := tappedClient(t, e, true)
	for _, tc := range []struct {
		types []events.Type
		gzip  bool
	}{
		{[]events.Type{events.TypeRunStatus, events.TypeCoordMessage, events.TypeAssistantNotice}, false},
		{[]events.Type{events.TypeRunStatus, events.TypeCoordMessage}, true},
	} {
		params := protocol.WorkspaceTimelineParams{WorkspaceID: string(e.ws.ID), Newest: true, Limit: 1000}
		for _, typ := range tc.types {
			params.Types = append(params.Types, string(typ))
		}
		pipe.got.Reset()
		var page protocol.WorkspaceTimelineResult
		if err := c.Call(protocol.MethodWorkspaceTimeline, params, &page); err != nil {
			t.Fatal(err)
		}
		if got := strings.HasPrefix(pipe.got.String(), "gzip "); got != tc.gzip || len(page.Events) < 100 {
			t.Fatalf("types %v: %d events, gzip frame %v, want %v", tc.types, len(page.Events), got, tc.gzip)
		}
	}
}

// The server's own gate: a request for a method off the list that asks for
// gzip anyway gets a JSON line.
func TestControlGzipRefusesUnlistedMethodsThatAsk(t *testing.T) {
	t.Parallel()
	e := seedLargeReads(t)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	if _, err := pipe.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"run.list","params":{},"accept_encoding":"gzip"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReaderSize(pipe, 1<<20).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if len(line) < protocol.GzipMinBytes || line[0] != '{' {
		t.Fatalf("run.list asking for gzip got %d bytes starting %q, want a JSON line", len(line), line[:1])
	}
}

// On the real server, a Lanes page of a workspace holding a session with a
// repository in another workspace stays a JSON line, even for a super admin
// who sees all of it; a page of a session wholly inside it compresses.
func TestControlGzipSendsMultiRepositoryPagesPlain(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := context.Background()
	for _, run := range []*domain.Run{w.runA, w.runUA} {
		for i := range 100 {
			if _, err := w.bus.Publish(ctx, events.Event{WorkspaceID: w.wsA.ID, RunID: run.ID, Payload: events.RunStatusPayload{
				From: domain.RunRunning, To: domain.RunRunning, StatusSeq: int64(i), Reason: fmt.Sprintf("agent-written reason %d", i),
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	client, err := w.e.dialWith(w.signers["S"], nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := &tappedPipe{subsystemPipe: openSubsystem(t, client, protocol.SubsystemControl, nil)}
	c := protocol.NewClient(pipe)
	c.AcceptGzip()
	for _, tc := range []struct {
		run  domain.RunID
		gzip bool
	}{{"", false}, {w.runA.ID, false}, {w.runUA.ID, true}} {
		pipe.got.Reset()
		var page protocol.WorkspaceTimelineResult
		if err := c.Call(protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{
			WorkspaceID: string(w.wsA.ID), RunID: string(tc.run), Newest: true, Limit: 1000,
			Types: []string{string(events.TypeRunStatus), string(events.TypeCoordMessage), string(events.TypeCoordMessageAcked)},
		}, &page); err != nil {
			t.Fatal(err)
		}
		if got := strings.HasPrefix(pipe.got.String(), "gzip "); got != tc.gzip || len(page.Events) < 100 {
			t.Fatalf("session %q: %d events, gzip frame %v, want %v", tc.run, len(page.Events), got, tc.gzip)
		}
	}
}

// Each gate of pageScope, on the real server and read as a super admin who
// sees everything: beside a page that compresses, one more event only some
// readers of the workspace see sends the page plain.
func TestControlGzipPageScopeGates(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := context.Background()
	publish := func(run domain.RunID, payload events.Payload) {
		t.Helper()
		if _, err := w.bus.Publish(ctx, events.Event{WorkspaceID: w.wsA.ID, RunID: run, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 100 {
		publish(w.runUA.ID, events.RunStatusPayload{From: domain.RunRunning, To: domain.RunRunning, StatusSeq: int64(i), Reason: fmt.Sprintf("agent-written reason %d", i)})
	}
	publish("", events.MemberChangedPayload{MemberID: w.b.ID, DisplayName: "B"})
	publish(w.runUA.ID, events.RunDeletedPayload{ExtraWorkspaceIDs: []domain.WorkspaceID{w.wsB.ID}})
	publish(w.runUA.ID, events.CoordMessagePayload{MessageID: "msg-1", WorkspaceID: w.wsA.ID, FromRunID: w.runUA.ID, ToRunID: noRun, Kind: "message"})

	client, err := w.e.dialWith(w.signers["S"], nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := &tappedPipe{subsystemPipe: openSubsystem(t, client, protocol.SubsystemControl, nil)}
	c := protocol.NewClient(pipe)
	c.AcceptGzip()
	for _, tc := range []struct {
		gate  string
		extra events.Type
		gzip  bool
	}{
		{"none", "", true},
		{"member sight", events.TypeMemberChanged, false},
		{"another workspace", events.TypeRunDeleted, false},
		{"unreadable run", events.TypeCoordMessage, false},
	} {
		types := []string{string(events.TypeRunStatus)}
		if tc.extra != "" {
			types = append(types, string(tc.extra))
		}
		pipe.got.Reset()
		var page protocol.WorkspaceTimelineResult
		if err := c.Call(protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{
			WorkspaceID: string(w.wsA.ID), Newest: true, Limit: 1000, Types: types,
		}, &page); err != nil {
			t.Fatal(err)
		}
		if got := strings.HasPrefix(pipe.got.String(), "gzip "); got != tc.gzip || len(page.Events) < 100 || (tc.extra != "" && len(page.Events) != 101) {
			t.Fatalf("%s: %d events, gzip frame %v, want %v", tc.gate, len(page.Events), got, tc.gzip)
		}
	}
}
