package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func acpFrame(t *testing.T, seq int64, it acphost.Item) protocol.ACPFrame {
	t.Helper()
	raw, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.ACPFrame{Seq: seq, Item: raw}
}

// An Enhanced run's log is one line per message, thought and tool call,
// however many segments and snapshots the session wrote for each.
func TestLogsPrintsAnEnhancedSessionAsLines(t *testing.T) {
	exit := 1
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u1", Text: "fix the tests", Complete: true}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindThought, Message: &acphost.Message{Role: "assistant", MessageID: "t1", Text: "hmm"}}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "Running \x1b[31mthe"}}),
		acpFrame(t, 4, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "go test ./...", Status: "pending"}}),
		acpFrame(t, 5, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: " suite\nnow.", Complete: true}}),
		acpFrame(t, 6, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "go test ./...", Status: "failed", ExitCode: &exit}}),
	}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: frames, OldestSeq: 1}},
	})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10, nil); err != nil {
		t.Fatal(err)
	}
	want := "user: fix the tests\nthought: hmm\nassistant: Running  [31mthe suite now.\ntool failed: go test ./... (exit 1)\n"
	if got := out.String(); got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 1, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "tool failed: go test ./... (exit 1)\n" {
		t.Fatalf("logs --limit 1 = %q", got)
	}
}

func TestLogsPrintsTerminalHistoryOldestFirst(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodTerminalHistory: {result: protocol.TerminalHistoryResult{Lines: []protocol.TerminalHistoryLine{{Text: "$ go test"}, {Text: "ok"}}, TruncatedBefore: true}},
	})
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "[earlier output has expired]\n$ go test\nok\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}

func TestLogsKeepsAReusedToolIDPerTurn(t *testing.T) {
	tool := func(turn int64, status string) acphost.Item {
		return acphost.Item{Turn: turn, Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "ls", Status: status}}
	}
	msg := acphost.Item{Turn: 1, Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "hi"}}
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, tool(1, "pending")),
		acpFrame(t, 2, msg),
		acpFrame(t, 3, tool(1, "completed")),
		acpFrame(t, 4, tool(2, "failed")),
	}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: frames, OldestSeq: 1}},
	})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "tool completed: ls\nassistant: hi\ntool failed: ls\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 1, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "tool failed: ls\n"; got != want {
		t.Fatalf("logs --limit 1 = %q, want %q", got, want)
	}
}

func TestLogsDropsEntriesBeforeAResetItem(t *testing.T) {
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u1", Text: "old"}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindReset}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u2", Text: "new"}}),
	}
	entries := acpLogOf(frames).entries
	if len(entries) != 1 || entries[0].line() != "user: new" {
		t.Fatalf("entries after reset = %d, want only the new message", len(entries))
	}
}

func TestLogsFollowsAnEmptyTerminalPageWithAContinuationCursor(t *testing.T) {
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			var p protocol.TerminalHistoryParams
			if json.Unmarshal(line, &req) != nil || json.Unmarshal(req.Params, &p) != nil {
				return
			}
			res := protocol.TerminalHistoryResult{}
			switch p.Before {
			case "":
				res = protocol.TerminalHistoryResult{HasMore: true, NextCursor: "c1"}
			case "c1":
				res = protocol.TerminalHistoryResult{Lines: []protocol.TerminalHistoryLine{{Text: "real"}}}
			}
			raw, _ := json.Marshal(res)
			out, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "real\n" {
		t.Fatalf("logs = %q, want the line after the empty page", got)
	}
}

func TestLogsStopsWhenTheTerminalCursorRepeats(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodTerminalHistory: {result: protocol.TerminalHistoryResult{HasMore: true, NextCursor: "same"}},
	})
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
}

func TestLogsFetchesTheFullItemOfATruncatedFrame(t *testing.T) {
	full := acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "the whole long message", Complete: true}})
	cut := acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "the whole"}})
	cut.Truncated = true
	history := &fakeReply{result: protocol.RunACPHistoryResult{Frames: []protocol.ACPFrame{cut}, OldestSeq: 1}}
	item := &fakeReply{result: protocol.RunACPItemResult{Item: full.Item}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunACPHistory: history, protocol.MethodRunACPItem: item})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: the whole long message\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}

	item.err = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "gone"}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 10, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: the whole [truncated; run.acp.item: rpc error -32601: gone]\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}

func TestLogsReadsBackToTheStartOfAResumedMessage(t *testing.T) {
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "start "}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "ls", Status: "completed"}}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "end", Complete: true}}),
	}
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		br := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(br)
			if err != nil {
				return
			}
			var req protocol.Request
			var params protocol.RunACPHistoryParams
			if json.Unmarshal(line, &req) != nil || json.Unmarshal(req.Params, &params) != nil {
				return
			}
			page := frames[1:]
			if params.BeforeSeq == 2 {
				page = frames[:1]
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			resp.Result, _ = json.Marshal(protocol.RunACPHistoryResult{Frames: page, OldestSeq: 1})
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 2, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: start end\ntool completed: ls\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}

// Nothing before a reset reaches the output, so paging stops at it.
func TestLogsStopPagingAtAReset(t *testing.T) {
	history := &fakeReply{result: protocol.RunACPHistoryResult{OldestSeq: 1, Frames: []protocol.ACPFrame{
		acpFrame(t, 5, acphost.Item{Kind: acphost.KindReset}),
		acpFrame(t, 6, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a2", Text: "fresh"}}),
	}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunACPHistory: history})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: fresh\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	if got := string(history.params()); strings.Contains(got, "before_seq") {
		t.Fatalf("history read past the reset: %s", got)
	}
}

type fakeACPStream struct{ io.ReadCloser }

func (fakeACPStream) Write(p []byte) (int, error) { return len(p), nil }

// acpStreamOf serves lines as one aether-acp stream, which then ends or,
// held, stays open as the server keeps a stream with no agent session.
func acpStreamOf(held bool, lines ...any) io.ReadWriteCloser {
	pr, pw := io.Pipe()
	go func() {
		for _, v := range lines {
			line, _ := json.Marshal(v)
			if _, err := pw.Write(append(line, '\n')); err != nil {
				return
			}
		}
		if !held {
			_ = pw.Close()
		}
	}()
	return fakeACPStream{pr}
}

// A follow goes on from the snapshot's last line, subscribes again after the
// last item it read whenever a stream ends, and stops at the end of what a
// session that has ended wrote.
func TestLogsFollowPrintsAnEnhancedSessionUntilItEnds(t *testing.T) {
	logsFollowPoll = time.Millisecond
	message := func(kind acphost.Kind, role, id, text string, complete bool) acphost.Item {
		return acphost.Item{Kind: kind, Message: &acphost.Message{Role: role, MessageID: id, Text: text, Complete: complete}}
	}
	tool := func(status string) acphost.Item {
		return acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "go test ./...", Status: status}}
	}
	plan := func(suite string) acphost.Item {
		return acphost.Item{Kind: acphost.KindPlan, Plan: []acphost.PlanEntry{{Content: "read the code", Status: "completed"}, {Content: "run the suite", Status: suite}}}
	}
	request := func(status string) acphost.Item {
		return acphost.Item{Kind: acphost.KindRequest, Request: &acphost.Request{ID: "q1", Kind: acphost.RequestPermission, Title: "Allow go test?", Status: status}}
	}
	notice := func(severity, title, description string) acphost.Item {
		return acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Severity: severity, Title: title, Description: description}}
	}
	var items []protocol.ACPFrame
	for i, it := range []acphost.Item{
		message(acphost.KindMessage, "user", "u1", "fix the tests", true),
		plan("pending"),
		request(acphost.RequestPending),
		tool("pending"),
		message(acphost.KindMessage, "assistant", "a1", "Running the", false),
		message(acphost.KindMessage, "assistant", "a1", " suite", false),
		tool("in_progress"),
		tool("in_progress"),
		request(acphost.RequestAnswered),
		message(acphost.KindMessage, "assistant", "a1", " now.", true),
		message(acphost.KindThought, "assistant", "t1", "hmm\x1b[2J", false),
		notice("error", "Turn interrupted", "The agent connection ended before the turn finished."),
		{Kind: acphost.KindReset},
		plan("completed"),
		notice("info", "Enhanced agent stopped", "closed"),
	} {
		items = append(items, acpFrame(t, int64(i+1), it))
	}
	run := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "r1", ACP: true, Status: string(domain.RunRunning)}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:        run,
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: items[:5], OldestSeq: 1}},
	})
	var after []int64
	open := func(req protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
		after = append(after, req.AfterSeq)
		if len(after) == 1 {
			lines := []any{protocol.DashAttachControl{Type: protocol.DashAttachControlFrame}}
			for _, f := range items[5:12] {
				lines = append(lines, f)
			}
			return acpStreamOf(false, lines...), protocol.ACPStreamResponse{OK: true}, nil
		}
		// The agent session is gone: what the log gained is replayed, and the
		// stream then stays open.
		written := items[:14]
		if len(after) > 2 {
			written = items
		}
		var replay []any
		for _, f := range written {
			if f.Seq > req.AfterSeq {
				replay = append(replay, f)
			}
		}
		run.answer(protocol.RunResult{Run: protocol.Run{ID: "r1", ACP: true, Status: string(domain.RunCompleted)}})
		return acpStreamOf(true, replay...), protocol.ACPStreamResponse{OK: true, Replay: len(replay)}, nil
	}
	var out bytes.Buffer
	if err := printLogs(c, &out, "r1", 10, open); err != nil {
		t.Fatal(err)
	}
	want := "user: fix the tests\n" +
		"plan: read the code (completed); run the suite (pending)\n" +
		"request pending: Allow go test?\n" +
		"tool pending: go test ./...\n" +
		"assistant: Running the suite\n" +
		"tool in_progress: go test ./...\n" +
		"request answered: Allow go test?\n" +
		"assistant:  now.\n" +
		"thought: hmm [2J\n" +
		"notice error: Turn interrupted: The agent connection ended before the turn finished.\n" +
		"[conversation reset]\n" +
		"plan: read the code (completed); run the suite (completed)\n" +
		"notice info: Enhanced agent stopped: closed\n"
	if got := out.String(); got != want {
		t.Fatalf("logs --follow = %q, want %q", got, want)
	}
	if len(after) != 3 || after[0] != 5 || after[1] != 12 {
		t.Fatalf("streams opened after %v, want one after 5, one after 12 and a last one", after)
	}
}

// A stream replays only the newest items, so a follow that fell further
// behind says that it left some out.
func TestLogsFollowMarksWhatItSkipped(t *testing.T) {
	message := func(seq int64, text string) protocol.ACPFrame {
		return acpFrame(t, seq, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: text, Text: text, Complete: true}})
	}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:        {result: protocol.RunResult{Run: protocol.Run{ID: "r1", ACP: true, Status: string(domain.RunCompleted)}}},
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: []protocol.ACPFrame{message(1, "one")}, OldestSeq: 1}},
	})
	var out bytes.Buffer
	err := printLogs(c, &out, "r1", 10, func(protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
		return acpStreamOf(true, message(5, "five"), message(6, "six")), protocol.ACPStreamResponse{OK: true, Replay: 2}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: one\n[conversation skipped]\nassistant: five\nassistant: six\n"; got != want {
		t.Fatalf("logs --follow = %q, want %q", got, want)
	}
}

func TestLogsFollowNamesAttachForAStandardSession(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet: {result: protocol.RunResult{Run: protocol.Run{ID: "r1", Status: string(domain.RunRunning)}}},
	})
	var out bytes.Buffer
	err := printLogs(c, &out, "r1", 10, func(protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
		t.Error("opened a conversation stream on a Standard session")
		return nil, protocol.ACPStreamResponse{}, io.EOF
	})
	if err == nil || !strings.Contains(err.Error(), "cloudfield attach r1 --read-only") || out.Len() != 0 {
		t.Fatalf("logs --follow on a Standard session = %v, printed %q", err, out.String())
	}
}
