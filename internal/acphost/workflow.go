package acphost

import (
	"encoding/json"
	"reflect"
	"slices"
	"time"
)

// workflowEmitEvery is the shortest time between two snapshots of one
// workflow's tool call, each of which writes the whole call to the item log.
var workflowEmitEvery = time.Second

const (
	maxWorkflowAgents  = 100
	maxWorkflowPhases  = 64
	maxWorkflowLogs    = 20
	maxWorkflowPreview = 240
	maxWorkflowSummary = 160
	maxWorkflowField   = 64
)

// Workflow is the progress Claude Code reports for a dynamic workflow: its
// phases and one row per agent it started. Every snapshot is the whole
// workflow.
type Workflow struct {
	Name   string          `json:"name,omitempty"`
	Phases []WorkflowPhase `json:"phases,omitempty"`
	Agents []WorkflowAgent `json:"agents,omitempty"`
	// Logs are the workflow's log lines, newest last.
	Logs []string `json:"logs,omitempty"`
	// Omitted counts the agent rows left out to keep the snapshot bounded.
	Omitted int `json:"omitted,omitempty"`
}

type WorkflowPhase struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

// WorkflowAgent is one agent of a workflow. Prompt, Result and Error are
// previews.
type WorkflowAgent struct {
	Index int    `json:"index"`
	Label string `json:"label"`
	// Phase is the Index of the agent's phase.
	Phase int    `json:"phase,omitempty"`
	Type  string `json:"type,omitempty"`
	Model string `json:"model,omitempty"`
	// State is queued, running, done, failed or skipped.
	State       string     `json:"state"`
	Tool        string     `json:"tool,omitempty"`
	ToolSummary string     `json:"tool_summary,omitempty"`
	Prompt      string     `json:"prompt,omitempty"`
	Result      string     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	Tokens      int64      `json:"tokens,omitempty"`
	ToolCalls   int64      `json:"tool_calls,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	DurationMS  int64      `json:"duration_ms,omitempty"`
	Cached      bool       `json:"cached,omitempty"`
	// Attempt is set from the second attempt on.
	Attempt int `json:"attempt,omitempty"`
}

func (a WorkflowAgent) finished() bool { return a.State != "queued" && a.State != "running" }

// brief keeps what labels the task and drops the workflow's rows.
func (t *ToolTask) brief() *ToolTask {
	if t == nil || t.Workflow == nil {
		return t
	}
	b := *t
	b.Workflow = &Workflow{Name: t.Workflow.Name}
	return &b
}

type workflowRow struct {
	Type            string  `json:"type"`
	Index           int     `json:"index"`
	Title           string  `json:"title"`
	Message         string  `json:"message"`
	Label           string  `json:"label"`
	PhaseIndex      int     `json:"phaseIndex"`
	AgentType       string  `json:"agentType"`
	Model           string  `json:"model"`
	State           string  `json:"state"`
	StartedAt       float64 `json:"startedAt"`
	Attempt         int     `json:"attempt"`
	LastToolName    string  `json:"lastToolName"`
	LastToolSummary string  `json:"lastToolSummary"`
	PromptPreview   string  `json:"promptPreview"`
	ResultPreview   string  `json:"resultPreview"`
	Error           string  `json:"error"`
	Skipped         bool    `json:"skipped"`
	Cached          bool    `json:"cached"`
	Tokens          float64 `json:"tokens"`
	ToolCalls       float64 `json:"toolCalls"`
	DurationMs      float64 `json:"durationMs"`
}

func (r workflowRow) agent() WorkflowAgent {
	cut := func(s string, n int) string {
		s, _ = cutTail(s, n)
		return s
	}
	a := WorkflowAgent{
		Index: r.Index, Label: cut(r.Label, maxLinkBytes), Phase: r.PhaseIndex,
		Type: cut(r.AgentType, maxWorkflowField), Model: cut(r.Model, maxWorkflowField), State: "running",
		Tool: cut(r.LastToolName, maxWorkflowField), ToolSummary: cut(r.LastToolSummary, maxWorkflowSummary),
		Prompt: cut(r.PromptPreview, maxWorkflowPreview), Result: cut(r.ResultPreview, maxWorkflowPreview), Error: cut(r.Error, maxWorkflowPreview),
		Tokens: int64(r.Tokens), ToolCalls: int64(r.ToolCalls), DurationMS: int64(r.DurationMs), Cached: r.Cached,
	}
	switch {
	case r.Skipped:
		a.State = "skipped"
	case r.State == "start" && r.StartedAt == 0:
		a.State = "queued"
	case r.State == "done":
		a.State = "done"
	case r.State == "error":
		a.State = "failed"
	}
	if r.StartedAt > 0 {
		at := time.UnixMilli(int64(r.StartedAt)).UTC()
		a.StartedAt = &at
	}
	if r.Attempt > 1 {
		a.Attempt = r.Attempt
	}
	return a
}

// workflowSnapshot builds a bounded snapshot from the rows of one frame,
// which are the whole workflow. Rows it cannot read are skipped.
func workflowSnapshot(name string, progress json.RawMessage, omitted int) *Workflow {
	w := &Workflow{Omitted: max(omitted, 0)}
	w.Name, _ = cutTail(name, maxLinkBytes)
	var rows []json.RawMessage
	_ = json.Unmarshal(progress, &rows)
	for _, raw := range rows {
		var r workflowRow
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		switch r.Type {
		case "workflow_phase":
			if len(w.Phases) < maxWorkflowPhases {
				title, _ := cutTail(r.Title, maxLinkBytes)
				w.Phases = append(w.Phases, WorkflowPhase{Index: r.Index, Title: title})
			}
		case "workflow_agent":
			w.Agents = append(w.Agents, r.agent())
		case "workflow_log":
			line, _ := cutTail(r.Message, maxWorkflowPreview)
			w.Logs = append(w.Logs, line)
		}
	}
	if over := len(w.Logs) - maxWorkflowLogs; over > 0 {
		w.Logs = w.Logs[over:]
	}
	if over := len(w.Agents) - maxWorkflowAgents; over > 0 {
		w.Omitted += over
		w.Agents = slices.DeleteFunc(w.Agents, func(a WorkflowAgent) bool {
			if over == 0 || !a.finished() {
				return false
			}
			over--
			return true
		})
		w.Agents = w.Agents[:maxWorkflowAgents]
	}
	return w
}

// workflowState is the newest snapshot of a workflow task. dirty says the
// task's tool call does not carry it yet; sent is when a snapshot of the
// call was last written for it; timer says a held snapshot's write is
// scheduled.
type workflowState struct {
	snap  *Workflow
	dirty bool
	sent  time.Time
	timer bool
}

func (s *Session) workflowFrameLocked(m sdkMessage) {
	if m.WorkflowName == "" && !present(m.WorkflowProgress) {
		return
	}
	w := s.workflows[m.TaskID]
	if w == nil {
		w = &workflowState{snap: &Workflow{}}
		// An adopted session's call carries what its log recorded.
		if call, ok := s.taskCalls[m.TaskID]; ok && call.Task.Workflow != nil {
			w.snap = call.Task.Workflow
		}
		s.workflows[m.TaskID] = w
	}
	next := w.snap
	if present(m.WorkflowProgress) {
		next = workflowSnapshot(w.snap.Name, m.WorkflowProgress, m.WorkflowOmitted)
	}
	if m.WorkflowName != "" {
		named := *next
		named.Name, _ = cutTail(m.WorkflowName, maxLinkBytes)
		next = &named
	}
	if !reflect.DeepEqual(next, w.snap) {
		w.snap, w.dirty = next, true
	}
}

// flushWorkflowLocked writes the launching tool call's snapshot when its
// workflow changed. A change within workflowEmitEvery of the last write
// waits until that time is up: the task's next frame can be minutes away.
func (s *Session) flushWorkflowLocked(id string) {
	w, call := s.workflows[id], s.taskCalls[id]
	if w == nil || !w.dirty || call.Task == nil {
		return
	}
	if wait := workflowEmitEvery - time.Since(w.sent); wait > 0 {
		if !w.timer {
			w.timer = true
			time.AfterFunc(wait, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				w.timer = false
				s.flushWorkflowLocked(id)
			})
		}
		return
	}
	w.sent = time.Now()
	// Output is a delta an earlier snapshot already carried.
	call.Output = ""
	s.emitLocked(Item{Kind: KindToolCall, ToolCall: &call})
}

func (s *Session) attachWorkflowLocked(task *ToolTask) *ToolTask {
	w := s.workflows[task.ID]
	if w == nil || task.Workflow == w.snap {
		return task
	}
	w.dirty = false
	with := *task
	with.Workflow = w.snap
	return &with
}
