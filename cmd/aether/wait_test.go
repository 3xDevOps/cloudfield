package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestWaitExitsWithTheRunsOutcome(t *testing.T) {
	timedOut := make(chan time.Time)
	close(timedOut)
	for _, tc := range []struct {
		name     string
		run      protocol.Run
		items    []protocol.AttentionItem
		deadline <-chan time.Time
		code     int
		lines    []string
	}{
		{name: "completed", run: protocol.Run{Status: "completed"}, code: waitFinished, lines: []string{"session r1 Completed\n"}},
		{name: "reported success", run: protocol.Run{Status: "needs-attention", Activity: "finished", Review: "ready"},
			items: []protocol.AttentionItem{{Kind: protocol.AttentionKindReady, RunID: "r1"}}, code: waitFinished},
		{name: "failed", run: protocol.Run{Status: "failed", Reason: "agent exited 1: boom"}, code: waitFailed,
			lines: []string{"session r1 Failed: agent exited 1: boom\n"}},
		{name: "permission request", run: protocol.Run{Status: "needs-attention", Activity: "needs-you"},
			items: []protocol.AttentionItem{permissionItem()}, code: waitNeedsYou,
			lines: []string{"r1 permission req-1: Run rm -rf build\n", "  aether answer r1 req-1 allow-1    # Allow once\n"}},
		{name: "turn ended without a report", run: protocol.Run{Status: "needs-attention", Activity: "waiting", Reason: "turn ended without a report"},
			code: waitWaiting, lines: []string{"session r1 Waiting: turn ended without a report\n", "  aether message r1 \"<reply>\"\n"}},
		{name: "answered", run: protocol.Run{Status: "needs-attention", Activity: "answered", Reason: "explained the retry policy"},
			code: waitWaiting, lines: []string{"session r1 Answered: explained the retry policy\n", "  aether message r1 \"<reply>\"\n"}},
		{name: "blocked", run: protocol.Run{Status: "needs-attention", Activity: "needs-you", Reason: "blocked: no token"}, code: waitNeedsYou,
			lines: []string{"session r1 Needs you: blocked: no token\n", "  it waits on another member; aether sessions r1 shows its state\n"}},
		{name: "disconnected", run: protocol.Run{Status: "needs-attention", Activity: "disconnected"}, code: waitNeedsYou},
		{name: "stalled", run: protocol.Run{Status: "needs-attention", Activity: "waiting", Reason: "stalled: no output or file changes for 10m"},
			code: waitWaiting, lines: []string{"  aether message r1 \"<reply>\"\n"}},
		{name: "waiting for agents", run: protocol.Run{Status: "needs-attention", Activity: "waiting-agents"}, deadline: timedOut, code: waitTimedOut},
		{name: "waiting on an unreadable condition", run: protocol.Run{Status: "needs-attention", Activity: "waiting-ci", Reason: "CI on #12",
			Wait: &domain.RunWait{Kind: domain.WaitSourceUnknown, Detail: "pr:o/r#12:checks: status unknown", Error: "gh: API rate limit exceeded"}},
			deadline: timedOut, code: waitTimedOut,
			lines: []string{"session r1 Waiting for CI (status unknown): CI on #12\n  pr:o/r#12:checks: status unknown: gh: API rate limit exceeded\n"}},
		{name: "broken wait", run: protocol.Run{Status: "needs-attention", Activity: "needs-you", Cause: "wait-fault",
			Reason: "wait broken: pr:o/r#12:checks resolved, but the agent was not woken"}, code: waitBroken,
			lines: []string{"session r1 Needs you: wait broken: pr:o/r#12:checks resolved, but the agent was not woken\n", "  aether message r1 \"<reply>\"\n"}},
		{name: "paused", run: protocol.Run{Status: "needs-attention", Activity: "needs-you", Paused: true}, deadline: timedOut, code: waitTimedOut},
		{name: "working", run: protocol.Run{Status: "running", Activity: "working"}, deadline: timedOut, code: waitTimedOut,
			lines: []string{"session r1 Working\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run.ID, tc.run.WorkspaceID = "r1", "ws1"
			c := fakeControlMethods(t, map[string]*fakeReply{
				protocol.MethodRunGet:            {result: protocol.RunResult{Run: tc.run}},
				protocol.MethodAttentionList:     {result: protocol.AttentionListResult{Items: tc.items}},
				protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
				protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
			})
			var out bytes.Buffer
			err := waitRun(c, &out, io.Discard, []string{"r1"}, false, make(chan struct{}), time.Hour, tc.deadline, time.Minute)
			code := 0
			var status *exitStatusError
			if errors.As(err, &status) {
				code = status.code
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("wait exit = %d (%v), want %d; output %q", code, err, tc.code, out.String())
			}
			for _, line := range tc.lines {
				if !strings.Contains(out.String(), line) {
					t.Errorf("wait printed %q, want the line %q", out.String(), line)
				}
			}
			if strings.Contains(out.String(), "always-1") {
				t.Errorf("wait offers an always option: %q", out.String())
			}
		})
	}
}

// Of several sessions the first in argument order with an outcome ends the
// wait and is the one printed. A timeout prints each session's line, and
// with --json nothing, so stdout never holds two documents.
func TestWaitOnSeveralSessions(t *testing.T) {
	timedOut := make(chan time.Time)
	close(timedOut)
	runs := map[string]protocol.Run{
		"r1": {Status: "running", Activity: "working"},
		"r2": {Status: "failed", Reason: "agent exited 1: boom"},
		"r3": {Status: "completed"},
		"r4": {Status: "needs-attention", Activity: "waiting-ci"},
	}
	for _, tc := range []struct {
		name   string
		ids    []string
		asJSON bool
		code   int
		out    string
		jsonID string
		err    string
	}{
		{name: "first with an outcome", ids: []string{"r1", "r2", "r3"}, code: waitFailed,
			out: "session r2 Failed: agent exited 1: boom\n", err: "session r2 failed"},
		{name: "first with an outcome as JSON", ids: []string{"r1", "r2", "r3"}, asJSON: true, code: waitFailed,
			jsonID: "r2", err: "session r2 failed"},
		{name: "timeout", ids: []string{"r1", "r4"}, code: waitTimedOut,
			out: "session r1 Working\nsession r4 Waiting for CI\n", err: "timed out after 1m0s waiting for sessions r1, r4"},
		{name: "timeout as JSON", ids: []string{"r1", "r4"}, asJSON: true, code: waitTimedOut,
			err: "timed out after 1m0s waiting for sessions r1, r4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lists atomic.Int32
			c := fakeControlMethods(t, map[string]*fakeReply{
				protocol.MethodRunGet: {byParams: func(params json.RawMessage) any {
					var p protocol.RunIDParams
					_ = json.Unmarshal(params, &p)
					r := runs[p.RunID]
					r.ID, r.WorkspaceID = p.RunID, "ws1"
					return protocol.RunResult{Run: r}
				}},
				protocol.MethodAttentionList: {byParams: func(json.RawMessage) any {
					lists.Add(1)
					return protocol.AttentionListResult{}
				}},
				protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
				protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
			})
			var out bytes.Buffer
			err := waitRun(c, &out, io.Discard, tc.ids, tc.asJSON, make(chan struct{}), time.Hour, timedOut, time.Minute)
			var status *exitStatusError
			if !errors.As(err, &status) || status.code != tc.code || err.Error() != tc.err {
				t.Fatalf("wait = %v, want exit %d: %s", err, tc.code, tc.err)
			}
			if tc.jsonID != "" {
				var v runView
				if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.ID != tc.jsonID {
					t.Errorf("wait printed %q (%v), want session %s as one JSON object", out.String(), err, tc.jsonID)
				}
			} else if out.String() != tc.out {
				t.Errorf("wait printed %q, want %q", out.String(), tc.out)
			}
			if n := lists.Load(); n != 1 {
				t.Errorf("attention.list ran %d times for sessions of one workspace, want 1", n)
			}
		})
	}
}

func TestParseWaitArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		ids     []string
		timeout time.Duration
		asJSON  bool
	}{
		{name: "one id", args: []string{"r1"}, ids: []string{"r1"}},
		{name: "flags between ids", args: []string{"r1", "--timeout", "1m", "r2", "--json"}, ids: []string{"r1", "r2"}, timeout: time.Minute, asJSON: true},
		{name: "flags first", args: []string{"--json", "r1", "r2"}, ids: []string{"r1", "r2"}, asJSON: true},
		{name: "an id given twice", args: []string{"r1", "r2", "r1"}, ids: []string{"r1", "r2"}},
		{name: "no id"},
		{name: "an empty id", args: []string{"r1", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, timeout, asJSON, err := parseWaitArgs(tc.args)
			if tc.ids == nil {
				if err == nil || !strings.HasPrefix(err.Error(), "usage: cloudfield wait ") {
					t.Fatalf("parseWaitArgs = %q, %v, want the usage error", ids, err)
				}
				return
			}
			if err != nil || !slices.Equal(ids, tc.ids) || timeout != tc.timeout || asJSON != tc.asJSON {
				t.Fatalf("parseWaitArgs = %q, %s, %v, %v, want %q, %s, %v", ids, timeout, asJSON, err, tc.ids, tc.timeout, tc.asJSON)
			}
		})
	}
}

// A wait on several sessions that filtered its subscription on one of them
// would hear of the others only at its recheck.
func TestWaitSubscribesToOneSessionOrEvery(t *testing.T) {
	if got := waitSubscription([]string{"r1"}).RunID; got != "r1" {
		t.Errorf("a wait on r1 subscribed to session %q, want r1", got)
	}
	if got := waitSubscription([]string{"r1", "r2"}).RunID; got != "" {
		t.Errorf("a wait on r1 and r2 subscribed to session %q, want every session", got)
	}
}

// Subscribed to every session's status events, the wait wakes only on the
// ones it names.
func TestWaitWakesOnlyOnItsSessions(t *testing.T) {
	for runID, want := range map[string]bool{"r2": true, "r9": false} {
		changed := make(chan struct{}, 1)
		signalLines(strings.NewReader(`{"type":"run.status","run_id":"`+runID+`"}`+"\n"), []string{"r1", "r2"}, changed)
		if _, woke := <-changed; woke != want {
			t.Errorf("a status event of %s woke the wait = %v, want %v", runID, woke, want)
		}
	}
}

// A status event wakes the wait instead of its recheck interval.
func TestWaitWakesOnAStatusEvent(t *testing.T) {
	run := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "running", Activity: "working"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:            run,
		protocol.MethodAttentionList:     {result: protocol.AttentionListResult{}},
		protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
	})
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- waitRun(c, &bytes.Buffer{}, io.Discard, []string{"r1"}, false, changed, time.Hour, nil, 0)
	}()
	for run.params() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	run.answer(protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "completed"}})
	changed <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait after the session completed = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a status event did not wake the wait")
	}
}

// A wait Aether cannot vouch for keeps aether wait going and is said once
// on stderr, however its source's error reads from poll to poll; --json
// keeps stdout to the run alone.
func TestWaitWarnsOnceOnAnUnknownSource(t *testing.T) {
	unknown := func(err string) protocol.RunResult {
		return protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "needs-attention", Activity: "waiting-ci",
			Wait: &domain.RunWait{Kind: domain.WaitSourceUnknown, Detail: "pr:o/r#12:checks: status unknown", Error: err}}}
	}
	run := &fakeReply{result: unknown("gh: API rate limit exceeded, resets in 40s")}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:            run,
		protocol.MethodAttentionList:     {result: protocol.AttentionListResult{}},
		protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
	})
	changed := make(chan struct{})
	var out, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- waitRun(c, &out, &stderr, []string{"r1"}, true, changed, time.Hour, nil, 0) }()
	for run.params() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	run.answer(unknown("gh: API rate limit exceeded, resets in 5s"))
	// A send is taken only between reads, so the second read sees the new
	// error before the run completes.
	changed <- struct{}{}
	changed <- struct{}{}
	run.answer(protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "completed"}})
	timeout := time.After(5 * time.Second)
	for ended := false; !ended; {
		select {
		case changed <- struct{}{}:
		case err := <-done:
			if err != nil {
				t.Fatalf("wait after the session completed = %v", err)
			}
			ended = true
		case <-timeout:
			t.Fatal("the wait did not end")
		}
	}
	if want := "session r1 still waits, but pr:o/r#12:checks: status unknown: gh: API rate limit exceeded, resets in 40s\n"; stderr.String() != want {
		t.Fatalf("stderr = %q, want once %q", stderr.String(), want)
	}
	var v runView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.State != "completed" {
		t.Fatalf("stdout = %q (%v), want the session as JSON", out.String(), err)
	}
}

// Once --timeout has cut the connection, the error a stalled call returns
// becomes a timeout; an outcome wait already reached keeps its own code.
func TestWaitCutConnectionTimesOut(t *testing.T) {
	stalled := errors.New("EOF")
	var status *exitStatusError
	if err := timedOutAfterCut(stalled, true, time.Minute, []string{"r1"}); !errors.As(err, &status) || status.code != waitTimedOut {
		t.Fatalf("cut connection = %v, want exit %d", err, waitTimedOut)
	}
	if err := timedOutAfterCut(stalled, false, time.Minute, []string{"r1"}); err != stalled {
		t.Fatalf("uncut connection = %v, want the call's error", err)
	}
	needsYou := &exitStatusError{code: waitNeedsYou, err: errors.New("session r1 needs you")}
	if err := timedOutAfterCut(needsYou, true, time.Minute, []string{"r1"}); err != needsYou {
		t.Fatalf("outcome after cut = %v, want it unchanged", err)
	}
}
