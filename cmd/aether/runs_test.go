package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestRenderRuns(t *testing.T) {
	archivedAt := "2026-09-10T00:00:00Z"
	runs := []protocol.Run{
		{ID: "r1", Status: "running", Activity: "working", Harness: "claude", Mode: "tui", MemberID: "m1", AccountMemberID: "m1", Title: "Title one", Task: "task one"},
		{ID: "r2", Status: "needs-attention", Activity: "needs-you", Harness: "claude", Mode: "acp", MemberID: "m2", AccountMemberID: "m1", RequestedAccountMemberID: "m2",
			Title: "Title two", Task: "task two"},
		{ID: "r4", Status: "needs-attention", Activity: "waiting-agents", Harness: "claude", Mode: "headless", UnackedMessages: 2, MemberID: "m1", AccountMemberID: "m1",
			Title: "Integrate", Task: "task four"},
		{ID: "r6", Status: "needs-attention", Activity: "failed", OutcomeUnseen: true, Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Parked", Task: "task six"},
		{ID: "r7", Status: "failed", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Ended", Task: "task seven"},
		{ID: "r8", Status: "needs-attention", Activity: "finished", Review: "ready", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Done", Task: "task eight"},
		{ID: "r9", Status: "completed", Review: "later", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Later", Task: "task nine"},
		{ID: "r10", Status: "needs-attention", Activity: "waiting-ci", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "CI", Task: "task ten",
			Wait: &domain.RunWait{Kind: domain.WaitSourceUnknown}},
		{ID: "r11", Status: "needs-attention", Activity: "idle", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Legacy", Task: "task eleven"},
		{ID: "r12", Status: "needs-attention", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Parked", Task: "task twelve"},
		{ID: "r13", Status: "needs-attention", Activity: "needs-you", Reason: "blocked: no token", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Blocked", Task: "task thirteen"},
		{ID: "r14", Status: "needs-attention", Activity: "needs-you", Reason: "blocked: no key", Paused: true, Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Paused", Task: "task fourteen"},
		{ID: "r15", Status: "needs-attention", Activity: "answered", Reason: "explained the retry policy", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Q&A", Task: "task fifteen"},
		{ID: "r16", Status: "needs-attention", Activity: "waiting-tools", ACP: true, Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Build", Task: "task sixteen"},
		{ID: "r17", Status: "needs-attention", Activity: "waiting-ci", ACP: true, ParentRunID: "r4", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Sub", Task: "task seventeen"},
		{ID: "r18", Status: "needs-attention", Activity: "failed", OutcomeUnseen: true, ParentRunID: "r4", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Held", Task: "task eighteen"},
		{ID: "r19", Status: "needs-attention", Activity: "failed", OutcomeUnseen: true, ParentRunID: "r4", Detached: true, Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Detached", Task: "task nineteen"},
		{ID: "r3", Status: "abandoned", Harness: "claude", MemberID: "m3", AccountMemberID: "m3", Title: "Title three", Task: "task three",
			ArchivedAt: &archivedAt},
	}
	memberName := func(id string) string { return id }
	overlapOf := map[string]string{"r2": "a.go with bob"}

	cases := []struct {
		name     string
		needsYou bool
		archived bool
		want     string
		wantIDs  []string
	}{
		{
			name: "default hides archived sessions",
			want: "ID   STATUS                           AGENT   MODE        MEMBER  ACCOUNT        PARENT         MAIL  OVERLAP        TITLE      TASK\n" +
				"r1   Working                          claude  standard    m1      m1                                                 Title one  task one\n" +
				"r2   Needs you                        claude  enhanced    m2      m1 (fallback)                       a.go with bob  Title two  task two\n" +
				"r4   Waiting for agents               claude  background  m1      m1                            2                    Integrate  task four\n" +
				"r6   Failed                           claude              m1      m1                                                 Parked     task six\n" +
				"r7   Failed                           claude              m1      m1                                                 Ended      task seven\n" +
				"r8   Finished: review                 claude              m1      m1                                                 Done       task eight\n" +
				"r9   Finished: review                 claude              m1      m1                                                 Later      task nine\n" +
				"r10  Waiting for CI (status unknown)  claude              m1      m1                                                 CI         task ten\n" +
				"r11  Waiting                          claude              m1      m1                                                 Legacy     task eleven\n" +
				"r12  Waiting                          claude              m1      m1                                                 Parked     task twelve\n" +
				"r13  Needs you                        claude              m1      m1                                                 Blocked    task thirteen\n" +
				"r14  Needs you                        claude              m1      m1                                                 Paused     task fourteen\n" +
				"r15  Answered                         claude              m1      m1                                                 Q&A        task fifteen\n" +
				"r16  Waiting for process              claude              m1      m1                                                 Build      task sixteen\n" +
				"r17  Waiting for CI                   claude              m1      m1             r4                                  Sub        task seventeen\n" +
				"r18  Failed                           claude              m1      m1             r4                                  Held       task eighteen\n" +
				"r19  Failed                           claude              m1      m1             r4 (detached)                       Detached   task nineteen\n",
		},
		{
			name:     "--needs-you hides archived, waiting, worker, ended, finished and held subsession runs",
			needsYou: true,
			want: "ID   STATUS     AGENT   MODE      MEMBER  ACCOUNT        PARENT         MAIL  OVERLAP        TITLE      TASK\n" +
				"r2   Needs you  claude  enhanced  m2      m1 (fallback)                       a.go with bob  Title two  task two\n" +
				"r6   Failed     claude            m1      m1                                                 Parked     task six\n" +
				"r13  Needs you  claude            m1      m1                                                 Blocked    task thirteen\n" +
				"r19  Failed     claude            m1      m1             r4 (detached)                       Detached   task nineteen\n",
		},
		{
			name:     "--archived shows only archived sessions",
			archived: true,
			wantIDs:  []string{"r3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderRuns(&buf, io.Discard, runs, memberName, overlapOf, nil, tc.needsYou, tc.archived); err != nil {
				t.Fatalf("renderRuns: %v", err)
			}
			if got := buf.String(); tc.want != "" && got != tc.want {
				t.Errorf("renderRuns() = %q, want %q", got, tc.want)
			}
			if tc.wantIDs != nil {
				rows := strings.Split(strings.TrimSpace(buf.String()), "\n")
				if len(rows) != len(tc.wantIDs)+1 {
					t.Fatalf("renderRuns() rows = %q, want a header and %d sessions", rows, len(tc.wantIDs))
				}
				for i, id := range tc.wantIDs {
					if fields := strings.Fields(rows[i+1]); len(fields) == 0 || fields[0] != id {
						t.Errorf("renderRuns() row = %q, want session %s", rows[i+1], id)
					}
				}
			}
		})
	}
}

func TestRunsRejectsNeedsYouAndArchivedTogether(t *testing.T) {
	err := runRuns([]string{"--needs-you", "--archived"})
	if err == nil {
		t.Fatal("--needs-you with --archived succeeded, want a usage error")
	}
	if !strings.Contains(err.Error(), "--needs-you and --archived cannot be used together") {
		t.Errorf("error = %v, want the exact combination message", err)
	}
}

func permissionItem() protocol.AttentionItem {
	return protocol.AttentionItem{Kind: protocol.AttentionKindPermission, RunID: "r1", WorkspaceID: "ws1", Reason: "Permission requested",
		Request: &protocol.AttentionRequest{ID: "req-1", Kind: "permission", Title: "Run rm -rf build", Options: []protocol.AttentionOption{
			{ID: "allow-1", Label: "Allow once", Kind: "allow_once"},
			{ID: "always-1", Label: "Always allow", Kind: "allow_always"},
			{ID: "reject-1", Label: "Reject", Kind: "reject_once"},
		}}}
}

func reportEvent(t *testing.T, runID, outcome, summary string) protocol.Event {
	t.Helper()
	raw, err := json.Marshal(events.TimelinePayload{Kind: events.TimelineReport, Outcome: outcome, Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Event{RunID: runID, Type: string(events.TypeTimeline), Payload: raw}
}

// A run's view carries the latest report and only the options aether
// answer accepts, and its text form ends with the commands that answer it.
func TestRunViewShowsReportAndAnswerableOptions(t *testing.T) {
	oldest := "2026-11-26T10:00:00Z"
	r := protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "needs-attention", Activity: "needs-you", Reason: "needs input: which build dir?",
		Branch: "aether/run-x", PendingInputs: []domain.RunInputRequest{{ID: "req-1", Kind: "permission"}},
		Harness: "claude", Mode: "acp", Model: "opus", ParentRunID: "r0", RootRunID: "r0", ChildPurpose: "fix the build", Detached: true, Children: "off",
		UnackedMessages: 2, OldestUnackedAt: &oldest, ControllerMemberID: "m1",
		WaitConditions: []protocol.WaitCondition{{ID: "wat-1", Spec: "pr:acme/app#7:checks", Kind: protocol.WaitKindPRChecks, State: "resolved", Outcome: "failed"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:        {result: protocol.RunResult{Run: r}},
		protocol.MethodAttentionList: {result: protocol.AttentionListResult{Items: []protocol.AttentionItem{permissionItem()}}},
		protocol.MethodWorkspaceList: {result: protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: "ws1", Name: "demo"}}}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{Events: []protocol.Event{
			reportEvent(t, "r1", "failure", "older"), reportEvent(t, "r1", "blocked", "no build dir"),
		}}},
		protocol.MethodMemberList: {result: protocol.MemberListResult{Members: []protocol.Member{{ID: "m1", DisplayName: "Ada"}}}},
	})
	var buf bytes.Buffer
	if err := runShow(c, &buf, "r1", true); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "r1", "workspace_id": "ws1", "workspace": "demo", "status": "Needs you", "state": "needs-attention",
		"reason": "needs input: which build dir?", "branch": "aether/run-x",
		"pending_inputs": []any{map[string]any{"kind": "permission", "request_id": "req-1", "options": []any{
			map[string]any{"id": "allow-1", "label": "Allow once", "kind": "allow_once"},
			map[string]any{"id": "reject-1", "label": "Reject", "kind": "reject_once"},
		}}},
		"report": map[string]any{"outcome": "blocked", "summary": "no build dir"},
		"agent":  "claude", "mode": "enhanced", "model": "opus", "parent_run_id": "r0", "root_run_id": "r0", "child_purpose": "fix the build",
		"detached": true, "children": "off", "unacked_messages": float64(2), "oldest_unacked_at": oldest, "controller_member_id": "m1",
		"wait_conditions": []any{map[string]any{"id": "wat-1", "revision": float64(0), "spec": "pr:acme/app#7:checks", "kind": "pr_checks",
			"state": "resolved", "outcome": "failed", "observed": map[string]any{}, "summary": "", "created_at": "",
			"consecutive_failures": float64(0), "delivery_attempts": float64(0), "expires_at": ""}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runs <id> --json = %s\nwant %v", buf.String(), want)
	}

	buf.Reset()
	if err := runShow(c, &buf, "r1", false); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	if !strings.HasPrefix(text, "run         r1\nstatus      Needs you\nreason      needs input: which build dir?\n") {
		t.Errorf("runs <id> = %q, want it to lead with the state and reason", text)
	}
	for _, line := range []string{"agent       claude\n", "mode        enhanced\n", "parent      r0 (detached)\n",
		"mail        2 not acknowledged, oldest sent 2026-11-26T10:00:00Z\n", "controller  Ada\n", "until       wat-1 pr:acme/app#7:checks failed\n",
		"report      blocked: no build dir\n", "pending     permission req-1\n",
		"r1 permission req-1: Run rm -rf build\n", "  aether answer r1 req-1 allow-1    # Allow once\n", "  aether answer r1 req-1 reject-1    # Reject\n"} {
		if !strings.Contains(text, line) {
			t.Errorf("runs <id> = %q, want the line %q", text, line)
		}
	}
	if strings.Contains(text, "always-1") {
		t.Errorf("runs <id> offers an always option: %q", text)
	}
}

// attention.list returns nothing for a session only another member can
// answer, so the one-session view says so in place of the answer commands.
func TestRunShowSaysWhenTheSessionWaitsOnAnotherMember(t *testing.T) {
	run := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "r5", WorkspaceID: "ws1", Status: "needs-attention", Activity: "needs-you",
		Reason: "blocked: no token", Harness: "codex", Mode: "tui"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:            run,
		protocol.MethodAttentionList:     {result: protocol.AttentionListResult{}},
		protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
		protocol.MethodMemberList:        {result: protocol.MemberListResult{}},
	})
	var buf bytes.Buffer
	if err := runShow(c, &buf, "r5", false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\nr5 waits on another member\n") {
		t.Errorf("sessions r5 = %q, want it to end saying it waits on another member", buf.String())
	}

	run.answer(protocol.RunResult{Run: protocol.Run{ID: "r5", WorkspaceID: "ws1", Status: "running", Activity: "working", Harness: "codex", Mode: "tui"}})
	buf.Reset()
	if err := runShow(c, &buf, "r5", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "another member") {
		t.Errorf("sessions r5 = %q, want no such line for a working session", buf.String())
	}
}

// A pipeline reading the --needs-you table sees the table alone: what the
// listed sessions need goes to stderr, and a session only another member can
// answer, for which attention.list returns nothing, says so.
func TestNeedsYouSaysOnStderrWhatEachListedSessionNeeds(t *testing.T) {
	asked := permissionItem()
	asked.RunID = "r2"
	attention := &fakeReply{result: protocol.AttentionListResult{Items: []protocol.AttentionItem{permissionItem(), asked}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunList: {result: protocol.RunListResult{Runs: []protocol.Run{
			{ID: "r1", Status: "running", Activity: "working", Harness: "claude", Mode: "tui", MemberID: "m1", AccountMemberID: "m1", Title: "Build", Task: "build"},
			{ID: "r2", Status: "needs-attention", Activity: "needs-you", Harness: "claude", Mode: "acp", MemberID: "m1", AccountMemberID: "m1", Title: "Clean", Task: "clean"},
			{ID: "r5", Status: "needs-attention", Activity: "needs-you", Reason: "blocked: no token", Harness: "codex", Mode: "tui", MemberID: "m2", AccountMemberID: "m2",
				Title: "Deploy", Task: "deploy"},
		}}},
		protocol.MethodMemberList:    {result: protocol.MemberListResult{Members: []protocol.Member{{ID: "m1", DisplayName: "Ada"}, {ID: "m2", DisplayName: "Bob"}}}},
		protocol.MethodRunOverlaps:   {result: protocol.RunOverlapsResult{}},
		protocol.MethodAttentionList: attention,
	})
	var stdout, stderr bytes.Buffer
	if err := listRuns(c, &stdout, &stderr, false, false, false); err != nil {
		t.Fatal(err)
	}
	if attention.params() != nil {
		t.Error("sessions without --needs-you called attention.list")
	}
	if got, want := stderr.String(), "\n2 sessions are waiting for a member: aether sessions --needs-you\n"; got != want {
		t.Errorf("sessions stderr = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	if err := listRuns(c, &stdout, &stderr, true, false, false); err != nil {
		t.Fatal(err)
	}
	wantTable := "ID  STATUS     AGENT   MODE      MEMBER  ACCOUNT  PARENT  MAIL  OVERLAP  TITLE   TASK\n" +
		"r2  Needs you  claude  enhanced  Ada     Ada                             Clean   clean\n" +
		"r5  Needs you  codex   standard  Bob     Bob                             Deploy  deploy\n"
	if got := stdout.String(); got != wantTable {
		t.Errorf("sessions --needs-you stdout = %q, want %q", got, wantTable)
	}
	wantNeeds := "\n" +
		"r2 permission req-1: Run rm -rf build\n" +
		"  aether answer r2 req-1 allow-1    # Allow once\n" +
		"  aether answer r2 req-1 reject-1    # Reject\n" +
		"r5 waits on another member; aether sessions r5 shows its state\n"
	if got := stderr.String(); got != wantNeeds {
		t.Errorf("sessions --needs-you stderr = %q, want %q", got, wantNeeds)
	}
}

func TestLatestReportCostsOneCallForARunThatNeverReported(t *testing.T) {
	server, client := net.Pipe()
	var calls []string
	go func() {
		defer server.Close() //nolint:errcheck
		br := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(br)
			if err != nil {
				return
			}
			var req protocol.Request
			var params protocol.WorkspaceTimelineParams
			if json.Unmarshal(line, &req) != nil || json.Unmarshal(req.Params, &params) != nil {
				return
			}
			calls = append(calls, params.RunID)
			res := protocol.WorkspaceTimelineResult{More: true, OlderSeq: 10}
			if params.RunID == "r1" {
				res.Events = []protocol.Event{reportEvent(t, "r1", "failure", "old")}
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			resp.Result, _ = json.Marshal(res)
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	got, err := reportsOf(c, []protocol.Run{{ID: "r1", WorkspaceID: "ws1"}, {ID: "r2", WorkspaceID: "ws1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]runReport{"r1": {Outcome: "failure", Summary: "old"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reportsOf = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(calls, []string{"r1", "r2"}) {
		t.Fatalf("timeline calls = %v, want one per session", calls)
	}
}

func TestASubsessionOutcomeReadsAsItsParents(t *testing.T) {
	done := protocol.Run{ID: "lead", ParentRunID: "root", Status: "needs-attention", Activity: "finished", Review: "ready"}
	if got := displayStatus(done); got != "Finished: reported to its parent" {
		t.Errorf("a subsession's success = %q", got)
	}
	top := protocol.Run{ID: "top", Status: "needs-attention", Activity: "finished", Review: "ready"}
	if got := displayStatus(top); got != "Finished: review" {
		t.Errorf("a top-level success = %q", got)
	}
	if needsYou(protocol.Run{ID: "leaf", ParentRunID: "lead", Status: "needs-attention", Activity: "failed", OutcomeUnseen: true}) {
		t.Error("a subsession's failure needs someone")
	}
}

func TestRenderRunsNamesTheLoginASessionIsOn(t *testing.T) {
	runs := []protocol.Run{
		{ID: "r1", Status: "running", Activity: "working", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", AccountLogin: "lgn-test1", AccountLoginName: "work", Title: "On work", Task: "one"},
		{ID: "r2", Status: "running", Activity: "working", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", AccountLogin: "lgn-test2", Title: "Removed", Task: "two"},
		{ID: "r3", Status: "running", Activity: "working", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Default", Task: "three"},
	}
	var buf bytes.Buffer
	if err := renderRuns(&buf, io.Discard, runs, func(id string) string { return id }, map[string]string{}, nil, false, false); err != nil {
		t.Fatal(err)
	}
	want := "ID  STATUS   AGENT   MODE  MEMBER  ACCOUNT              PARENT  MAIL  OVERLAP  TITLE    TASK\n" +
		"r1  Working  claude        m1      m1, login work                              On work  one\n" +
		"r2  Working  claude        m1      m1, login lgn-test2                         Removed  two\n" +
		"r3  Working  claude        m1      m1                                          Default  three\n"
	if buf.String() != want {
		t.Errorf("renderRuns =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestRunViewCarriesTheLoginASessionIsOn(t *testing.T) {
	for _, tc := range []struct {
		run  protocol.Run
		json map[string]any
		line string
	}{
		{protocol.Run{ID: "r1", Status: "running", AccountMemberID: "m1", AccountLogin: "lgn-test1", AccountLoginName: "console", AccountLoginPerUse: true},
			map[string]any{"account_login": "lgn-test1", "account_login_name": "console", "account_login_per_use": true},
			"login      console (lgn-test1), billed per use\n"},
		{protocol.Run{ID: "r2", Status: "running", AccountMemberID: "m1", AccountLogin: "lgn-test2"},
			map[string]any{"account_login": "lgn-test2"},
			"login      lgn-test2\n"},
	} {
		v := viewOf(tc.run, "demo", nil, nil)
		var buf bytes.Buffer
		if err := writeJSON(&buf, v); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for k, want := range tc.json {
			if got[k] != want {
				t.Errorf("sessions %s --json %s = %v, want %v", tc.run.ID, k, got[k], want)
			}
		}
		buf.Reset()
		if err := printRunView(&buf, v, nil, ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), tc.line) {
			t.Errorf("sessions %s =\n%s\nwant the line %q", tc.run.ID, buf.String(), tc.line)
		}
	}
}
