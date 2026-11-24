package acphost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
)

func workflowEvery(t *testing.T, d time.Duration) {
	t.Helper()
	old := workflowEmitEvery
	workflowEmitEvery = d
	t.Cleanup(func() { workflowEmitEvery = old })
}

func workflowRowOf(index int, state string, more map[string]any) map[string]any {
	row := map[string]any{"type": "workflow_agent", "index": index, "label": "agent", "state": state}
	for k, v := range more {
		row[k] = v
	}
	return row
}

// workflowProgress sends a task_progress frame of wf-1, a plain one when it
// has no rows.
func (m *mockAgent) workflowProgress(rows ...map[string]any) {
	frame := map[string]any{"type": "system", "subtype": "task_progress", "task_id": "wf-1"}
	if len(rows) > 0 {
		frame["workflow_progress"] = rows
	}
	m.sdk(frame)
}

func taskSnapshots(t *testing.T, s *Session) []ToolTask {
	t.Helper()
	var tasks []ToolTask
	for _, it := range ofKind(items(t, s), KindToolCall) {
		if it.ToolCall.Task != nil {
			tasks = append(tasks, *it.ToolCall.Task)
		}
	}
	return tasks
}

func agentStates(task ToolTask) string {
	if task.Workflow == nil {
		return "none"
	}
	var states []string
	for _, a := range task.Workflow.Agents {
		states = append(states, a.State)
	}
	return strings.Join(states, ",")
}

// promptedWorkflow runs frames inside the first prompt and waits for the
// session to go idle.
func promptedWorkflow(t *testing.T, frames func(m *mockAgent)) *Session {
	t.Helper()
	shortenFollowUp(t, 50*time.Millisecond)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		if m.prompts() == 1 {
			frames(m)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitFinished(t)
	return s
}

func TestWorkflowProgressFromClaudeCodeLandsOnTheLaunchingCall(t *testing.T) {
	workflowEvery(t, 0)
	data, err := os.ReadFile(filepath.Join("testdata", "claude_workflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := promptedWorkflow(t, func(m *mockAgent) {
		for sc := bufio.NewScanner(bytes.NewReader(data)); sc.Scan(); {
			var frame map[string]any
			if err := json.Unmarshal(sc.Bytes(), &frame); err != nil {
				t.Error(err)
				return
			}
			m.sdk(frame)
			if frame["workflow_name"] != nil {
				m.launchTool(frame["tool_use_id"].(string), frame["task_id"].(string))
			}
		}
	})
	tasks := taskSnapshots(t, s)
	var seen []string
	for _, task := range tasks {
		if task.ID != "wjnp2u1at" || task.Workflow == nil || task.Workflow.Name != "cap-demo" {
			t.Fatalf("task snapshot %+v, want workflow cap-demo of wjnp2u1at", task)
		}
		seen = append(seen, task.Status+":"+agentStates(task))
	}
	want := []string{"running:", "running:running,queued", "running:running,running", "running:done,running",
		"running:done,done,running", "running:done,done,done", "completed:done,done,done"}
	if !slices.Equal(seen, want) {
		t.Fatalf("snapshots %q\nwant      %q", seen, want)
	}
	final := tasks[len(tasks)-1]
	started := time.UnixMilli(1795503355718).UTC()
	wantAgent := WorkflowAgent{Index: 3, Label: "verify:all", Phase: 2, Model: "claude-opus-5-5", State: "done", Tool: "Read", ToolSummary: "/etc/hostname",
		Prompt: "Read the file /etc/hostname and reply with its content.", Result: "example-host", Tokens: 12071, ToolCalls: 1, StartedAt: &started, DurationMS: 3186}
	if got := final.Workflow.Agents[2]; got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("agent %+v, want it started at %v", got, started)
	} else if got.StartedAt = &started; got != wantAgent {
		t.Fatalf("agent %+v\nwant  %+v", got, wantAgent)
	}
	if phases := final.Workflow.Phases; !slices.Equal(phases, []WorkflowPhase{{1, "Find"}, {2, "Verify"}}) || final.Type != "local_workflow" {
		t.Fatalf("final task %+v with phases %+v", final, phases)
	}
	if starts := ofKind(items(t, s), KindTurnStart); len(starts) != 1 {
		t.Fatalf("%d turns, want the prompt's only", len(starts))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.taskCalls) != 0 {
		t.Fatalf("%d tool calls still tracked as running tasks", len(s.taskCalls))
	}
}

func TestWorkflowProgressBeforeItsToolCallIsKeptForIt(t *testing.T) {
	workflowEvery(t, time.Hour)
	started := map[string]any{"type": "system", "subtype": "task_started", "task_id": "wf-1", "task_type": "local_workflow", "workflow_name": "demo"}
	t.Run("running", func(t *testing.T) {
		s := promptedWorkflow(t, func(m *mockAgent) {
			m.sdk(started)
			m.workflowProgress(workflowRowOf(1, "progress", nil))
			m.launchTool("t1", "wf-1")
		})
		tasks := taskSnapshots(t, s)
		if len(tasks) != 1 || tasks[0].Status != "running" || tasks[0].Workflow == nil || tasks[0].Workflow.Name != "demo" || agentStates(tasks[0]) != "running" {
			t.Fatalf("snapshots %+v, want the launch to carry workflow demo with a running agent", tasks)
		}
	})
	t.Run("ended", func(t *testing.T) {
		s := promptedWorkflow(t, func(m *mockAgent) {
			m.sdk(started)
			m.workflowProgress(workflowRowOf(1, "progress", nil))
			m.sdk(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "wf-1", "status": "failed", "workflow_progress_omitted": 3,
				"workflow_progress": []any{workflowRowOf(1, "error", map[string]any{"error": "boom"})}})
			m.launchTool("t1", "wf-1")
		})
		tasks := taskSnapshots(t, s)
		if len(tasks) != 1 || tasks[0].Status != "failed" || agentStates(tasks[0]) != "failed" {
			t.Fatalf("snapshots %+v, want one failed call with its failed agent", tasks)
		}
		if w := tasks[0].Workflow; w.Name != "demo" || w.Omitted != 3 || w.Agents[0].Error != "boom" {
			t.Fatalf("workflow %+v", w)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.workflows) != 0 {
			t.Fatalf("%d workflows still held", len(s.workflows))
		}
	})
}

func TestWorkflowSnapshotsAreRateLimitedAndFlushed(t *testing.T) {
	workflowEvery(t, time.Hour)
	shortenWaits(t, 300*time.Millisecond, 15*time.Minute)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		m.launchTool("t1", "wf-1")
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startWorkflow(t, m)
	states := func() []string {
		var out []string
		for _, task := range taskSnapshots(t, s) {
			out = append(out, task.Status+":"+agentStates(task))
		}
		return out
	}
	held := func(state string) {
		t.Helper()
		waitFor(t, "a held "+state+" snapshot", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			w := s.workflows["wf-1"]
			return w != nil && w.dirty && w.snap.Agents[0].State == state
		})
	}

	m.workflowProgress(workflowRowOf(1, "start", nil))
	m.workflowProgress(workflowRowOf(1, "start", map[string]any{"startedAt": 1795503333404}))
	m.workflowProgress(workflowRowOf(1, "error", map[string]any{"skipped": true, "error": "skipped by user"}))
	held("skipped")
	if got, want := states(), []string{"running:none", "running:queued"}; !slices.Equal(got, want) {
		t.Fatalf("snapshots %q, want %q", got, want)
	}

	s.mu.Lock()
	s.workflows["wf-1"].sent = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()
	m.workflowProgress()
	waitFor(t, "the held snapshot", func() bool { return len(states()) == 3 })

	m.workflowProgress(workflowRowOf(1, "done", nil))
	held("done")
	m.workflowDone()
	want := []string{"running:none", "running:queued", "running:skipped", "completed:done"}
	waitFor(t, "the final snapshot", func() bool { return len(states()) == len(want) })
	if got := states(); !slices.Equal(got, want) {
		t.Fatalf("snapshots %q, want %q", got, want)
	}
	rec.waitFinished(t)
	if starts := ofKind(items(t, s), KindTurnStart); len(starts) != 1 {
		t.Fatalf("%d turns, want the prompt's only", len(starts))
	}
}

func TestHeldWorkflowSnapshotIsWrittenWithoutAnotherFrame(t *testing.T) {
	workflowEvery(t, 100*time.Millisecond)
	shortenWaits(t, 300*time.Millisecond, 15*time.Minute)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		m.launchTool("t1", "wf-1")
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, _ := startWorkflow(t, m)
	m.workflowProgress(workflowRowOf(1, "progress", nil))
	m.workflowProgress(workflowRowOf(1, "done", nil))
	waitFor(t, "the held snapshot", func() bool {
		tasks := taskSnapshots(t, s)
		return len(tasks) == 3 && tasks[2].Status == "running" && agentStates(tasks[2]) == "done"
	})
}

func TestWorkflowSnapshotIsBounded(t *testing.T) {
	long := strings.Repeat("é\x01", 4<<10)
	var rows []map[string]any
	for i := 1; i <= 130; i++ {
		state := "progress"
		if i <= 40 {
			state = "done"
		}
		rows = append(rows, workflowRowOf(i, state, map[string]any{"label": long, "agentType": long, "model": long, "lastToolName": long,
			"lastToolSummary": long, "promptPreview": long, "resultPreview": long, "error": long}))
	}
	for i := range 30 {
		rows = append(rows, map[string]any{"type": "workflow_log", "message": strings.Repeat("x", i)})
	}
	rows = append(rows, map[string]any{"type": "workflow_agent", "index": "not a number"})
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	w := workflowSnapshot(long, raw, 2)
	if len(w.Agents) != maxWorkflowAgents || w.Omitted != 32 || w.Agents[0].Index != 31 || w.Agents[10].State != "running" {
		t.Fatalf("%d agents from index %d, %d omitted; want the oldest 30 finished rows dropped", len(w.Agents), w.Agents[0].Index, w.Omitted)
	}
	if a := w.Agents[0]; len(a.Result) > maxWorkflowPreview || len(a.Result) < maxWorkflowPreview-2 || !utf8.ValidString(a.Result) || len(a.Label) > maxLinkBytes || len(w.Name) > maxLinkBytes {
		t.Fatalf("agent %+v is not cut on a rune boundary", a)
	}
	if len(w.Logs) != maxWorkflowLogs || len(w.Logs[maxWorkflowLogs-1]) != 29 {
		t.Fatalf("logs %q, want the newest %d", w.Logs, maxWorkflowLogs)
	}

	it := Item{Kind: KindToolCall, ToolCall: &ToolCall{ID: "t1", Title: "Workflow", Task: &ToolTask{ID: "wf-1", Type: "local_workflow", Status: "running", Workflow: w}}}
	b, err := it.encode()
	if err != nil || len(b) > MaxItemBytes {
		t.Fatalf("encoded item is %d bytes, %v", len(b), err)
	}
	it.ToolCall.Task.Workflow = w
	b, truncated, err := it.Wire(32 << 10)
	var cut Item
	if err != nil || !truncated || len(b) > 32<<10 || json.Unmarshal(b, &cut) != nil {
		t.Fatalf("Wire: %d bytes, truncated %v, %v", len(b), truncated, err)
	}
	if rows := cut.ToolCall.Task.Workflow; len(rows.Agents) != maxWorkflowAgents || rows.Omitted != 32 || rows.Agents[10].State != "running" || rows.Agents[0].Result != "" || rows.Logs != nil {
		t.Fatalf("cut workflow %+v, want every row's label and state without previews", rows)
	}
	b, truncated, err = it.Wire(1 << 10)
	if err != nil || !truncated || len(b) > 1<<10 {
		t.Fatalf("Wire: %d bytes, truncated %v, %v", len(b), truncated, err)
	}
	var got Item
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	task := got.ToolCall.Task
	if task.ID != "wf-1" || task.Status != "running" || task.Workflow == nil || task.Workflow.Name != w.Name || task.Workflow.Agents != nil || task.Workflow.Omitted != 0 {
		t.Fatalf("brief task %+v with workflow %+v, want the name only", task, task.Workflow)
	}
}

func TestAdoptedSessionKeepsRecordingWorkflowProgress(t *testing.T) {
	workflowEvery(t, time.Hour)
	shortenFollowUp(t, 50*time.Millisecond)
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1},
		{Kind: KindToolCall, Turn: 1, ToolCall: &ToolCall{ID: "t1", Title: "Workflow", Status: "completed",
			Task: &ToolTask{ID: "wf-1", Type: "local_workflow", Status: "running", Workflow: &Workflow{Name: "demo"}}}},
		{Kind: KindBackground, Turn: 1, Tasks: []Task{{ID: "wf-1", Type: "local_workflow", Holds: true}}},
		{Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"},
	} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, rec := startMock(t, m, Config{LogPath: path, ClaudeCode: true, Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(),
	}})
	rec.waitStates(t, "working:background")
	m.workflowProgress(workflowRowOf(1, "progress", nil))
	last := func() ToolTask {
		tasks := taskSnapshots(t, s)
		return tasks[len(tasks)-1]
	}
	waitFor(t, "the progress on the adopted call", func() bool { return agentStates(last()) == "running" })
	if task := last(); task.Workflow.Name != "demo" || task.Status != "running" {
		t.Fatalf("adopted task %+v with workflow %+v", task, task.Workflow)
	}
	m.workflowDone()
	waitFor(t, "the adopted call's end", func() bool { return last().Status == "completed" })
	if task := last(); task.Workflow.Name != "demo" || agentStates(task) != "running" {
		t.Fatalf("ended task %+v with workflow %+v", task, task.Workflow)
	}
}

func TestWorkflowToolOnlyChangesWaitLongerThanStateChanges(t *testing.T) {
	workflowEvery(t, 50*time.Millisecond)
	shortenWaits(t, 300*time.Millisecond, 15*time.Minute)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		m.launchTool("t1", "wf-1")
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, _ := startWorkflow(t, m)
	m.workflowProgress(workflowRowOf(1, "progress", nil))
	waitFor(t, "the first rows", func() bool { return len(taskSnapshots(t, s)) == 2 })
	m.workflowProgress(workflowRowOf(1, "progress", map[string]any{"lastToolName": "Bash", "tokens": 10}))
	time.Sleep(4 * workflowEmitEvery)
	if tasks := taskSnapshots(t, s); len(tasks) != 2 {
		t.Fatalf("%d snapshots, want the tool-only change held", len(tasks))
	}
	m.workflowProgress(workflowRowOf(1, "done", map[string]any{"lastToolName": "Bash", "tokens": 10}))
	waitFor(t, "the state change", func() bool {
		tasks := taskSnapshots(t, s)
		return len(tasks) == 3 && agentStates(tasks[2]) == "done" && tasks[2].Workflow.Agents[0].Tool == "Bash"
	})
}
