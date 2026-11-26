// Package sessioneval measures how a session's agent reacts to the role card
// and the live guidance behind it. It drives a real agent through the real
// in-container command and MCP bridge against a fake coordination socket, so
// every request the agent makes is recorded and nothing is ever started.
package sessioneval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

const (
	workspaceID = "ws-eval"
	memberID    = "mem-eval"
	memberName  = "eval"
)

// Call is one request the agent made on the coordination socket.
type Call struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	// Code and Error are the refusal the fake answered with.
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	// Replay marks a retry that got the receipt of an earlier request.
	Replay bool `json:"replay,omitempty"`
}

// Session is the run the fake socket belongs to.
type Session struct {
	RunID string
	// Parent is the run that started this one as its subsession.
	Parent string
	Task   string
}

// Coord is a fake wire-v3 coordination socket for one session.
type Coord struct {
	Path    string
	session Session
	ln      net.Listener
	closed  chan struct{}

	mu       sync.Mutex
	calls    []Call
	children []protocol.Child
	receipts map[string]receipt
}

// NewCoord listens on the session socket name inside dir.
func NewCoord(dir string, s Session) (*Coord, error) {
	path := filepath.Join(dir, coordtransport.SocketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sessioneval: listen on %s: %w", path, err)
	}
	c := &Coord{Path: path, session: s, ln: ln, closed: make(chan struct{}), receipts: make(map[string]receipt)}
	go c.accept()
	return c, nil
}

func (c *Coord) Close() {
	close(c.closed)
	_ = c.ln.Close()
}

// Calls returns the requests received so far, in order.
func (c *Coord) Calls() []Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (c *Coord) accept() {
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		go c.serve(conn)
	}
}

func (c *Coord) serve(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // nothing to report to
	r := bufio.NewReader(conn)
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		var req protocol.Request
		if json.Unmarshal(line, &req) != nil {
			return
		}
		resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
		result, replay, rpcErr := c.answer(req)
		call := Call{Method: req.Method, Params: req.Params, Replay: replay}
		if rpcErr != nil {
			resp.Error, call.Code, call.Error = rpcErr, rpcErr.Code, rpcErr.Message
		} else if resp.Result, err = json.Marshal(result); err != nil {
			return
		}
		c.mu.Lock()
		c.calls = append(c.calls, call)
		c.mu.Unlock()
		out, err := json.Marshal(resp)
		if err != nil {
			return
		}
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

func decode[T any](method string, raw json.RawMessage) (T, *protocol.Error) {
	var p T
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, &protocol.Error{Code: protocol.CodeInvalidParams, Message: method + ": " + err.Error()}
	}
	return p, nil
}

func (c *Coord) answer(req protocol.Request) (result any, replay bool, rpcErr *protocol.Error) {
	switch req.Method {
	case protocol.MethodCoordInbox:
		p, perr := decode[protocol.CoordInboxParams](req.Method, req.Params)
		if perr != nil {
			return nil, false, perr
		}
		// The real socket holds an empty inbox read open for wait_seconds.
		select {
		case <-time.After(time.Duration(min(max(p.WaitSeconds, 0), protocol.CoordMaxInboxWaitSeconds)) * time.Second):
		case <-c.closed:
		}
		return protocol.CoordInboxResult{Messages: []protocol.CoordMessage{}}, false, nil
	case protocol.MethodMemorySearch:
		return protocol.MemorySearchResult{Items: []protocol.MemoryItem{}}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch req.Method {
	case protocol.MethodCoordStatus:
		return c.status(), false, nil
	case protocol.MethodCoordSend:
		return c.once(req, func(n int) (any, *protocol.Error) {
			return protocol.CoordSendResult{MessageID: fmt.Sprintf("msg-eval-%d", n), RecipientStatus: "running"}, nil
		})
	case protocol.MethodCoordAsk:
		return c.once(req, func(n int) (any, *protocol.Error) {
			return protocol.CoordAskResult{QuestionID: fmt.Sprintf("q-eval-%d", n), RecipientStatus: "running"}, nil
		})
	case protocol.MethodCoordReply:
		return c.once(req, func(n int) (any, *protocol.Error) {
			return protocol.CoordReplyResult{MessageID: fmt.Sprintf("msg-eval-%d", n)}, nil
		})
	case protocol.MethodCoordReport:
		return c.once(req, func(n int) (any, *protocol.Error) { return c.report(req, n) })
	case protocol.MethodChildOptions:
		return c.options(), false, nil
	case protocol.MethodChildStart:
		return c.once(req, func(int) (any, *protocol.Error) { return c.start(req) })
	case protocol.MethodChildList:
		p, perr := decode[protocol.ChildListParams](req.Method, req.Params)
		if perr != nil {
			return nil, false, perr
		}
		out := protocol.ChildListResult{Children: []protocol.Child{}}
		for _, child := range c.children {
			if p.RunID == "" || p.RunID == child.RunID {
				out.Children = append(out.Children, child)
			}
		}
		return out, false, nil
	case protocol.MethodChildCancel:
		p, perr := decode[protocol.ChildRunParams](req.Method, req.Params)
		if perr != nil {
			return nil, false, perr
		}
		i := slices.IndexFunc(c.children, func(child protocol.Child) bool { return child.RunID == p.RunID })
		if i < 0 {
			return nil, false, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("child.cancel: no subsession %q", p.RunID)}
		}
		c.children[i].State = protocol.ChildCancelled
		return c.children[i], false, nil
	}
	return nil, false, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + req.Method}
}

type receipt struct {
	params json.RawMessage
	result any
}

// once answers a mutation as the real child.start does: a retry of its
// idempotency key gets the first receipt again, and the key with other
// inputs is refused.
func (c *Coord) once(req protocol.Request, do func(n int) (any, *protocol.Error)) (any, bool, *protocol.Error) {
	p, perr := decode[struct {
		Key string `json:"idempotency_key"`
	}](req.Method, req.Params)
	if perr != nil {
		return nil, false, perr
	}
	if p.Key == "" {
		return nil, false, &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": idempotency_key is required"}
	}
	key := req.Method + "\x00" + p.Key
	if first, ok := c.receipts[key]; ok {
		if !bytes.Equal(first.params, req.Params) {
			return nil, false, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("%s: idempotency key %q was already used with other inputs; use a new key", req.Method, p.Key)}
		}
		return first.result, true, nil
	}
	result, rpcErr := do(len(c.receipts) + 1)
	if rpcErr != nil {
		return nil, false, rpcErr
	}
	c.receipts[key] = receipt{req.Params, result}
	return result, false, nil
}

func (c *Coord) status() protocol.CoordStatusResult {
	capabilities := []string{protocol.MethodCoordStatus, protocol.MethodMemorySearch,
		protocol.MethodChildOptions, protocol.MethodChildList, protocol.MethodChildCancel}
	if c.session.Parent == "" {
		capabilities = append(capabilities, protocol.MethodChildStart)
	}
	capabilities = append(capabilities, protocol.MethodCoordSend, protocol.MethodCoordInbox,
		protocol.MethodCoordAsk, protocol.MethodCoordReply, protocol.MethodCoordReport)
	task := c.session.Task
	out := protocol.CoordStatusResult{
		WireVersion: protocol.CoordWireVersion, RunID: c.session.RunID, WorkspaceID: workspaceID, MemberID: memberID,
		Task: task[:min(len(task), protocol.CoordMaxStatusTaskBytes)], TaskBytes: len(task),
		TaskTruncated: len(task) > protocol.CoordMaxStatusTaskBytes,
		ParentRunID:   c.session.Parent, Peers: []protocol.CoordPeer{}, Capabilities: capabilities,
	}
	if c.session.Parent != "" {
		out.Peers = append(out.Peers, protocol.CoordPeer{RunID: c.session.Parent, MemberID: memberID, State: protocol.CoordPeerLineage, RunStatus: "running"})
		out.PeerTotal = 1
	}
	return out
}

// refusal is the reason internal/children gives a subsession that asks for
// subsessions of its own; empty when this session may start them.
func (c *Coord) refusal() string {
	if c.session.Parent == "" {
		return ""
	}
	return fmt.Sprintf("session %s is itself a subsession of %s, and subsessions go one level deep; do the work yourself or message %s",
		c.session.RunID, c.session.Parent, c.session.Parent)
}

func (c *Coord) unfinished() int {
	n := 0
	for _, child := range c.children {
		if child.State == protocol.ChildStarting {
			n++
		}
	}
	return n
}

func (c *Coord) options() protocol.ChildOptionsResult {
	out := protocol.ChildOptionsResult{Allowed: c.refusal() == "", Reason: c.refusal(), OwnerID: memberID,
		Limits: protocol.ChildLimits{MaxLive: 8}, Choices: []protocol.ChildChoice{}}
	if !out.Allowed {
		return out
	}
	out.Busy = c.unfinished()
	for _, agent := range [][2]string{{"claude", "Claude Code"}, {"codex", "Codex"}} {
		out.Choices = append(out.Choices, protocol.ChildChoice{
			AccountMemberID: memberID, AccountName: memberName, Harness: agent[0], DisplayName: agent[1],
			Modes: []string{"enhanced", "standard", "background"}, DefaultMode: "enhanced", Status: "ready",
		})
	}
	return out
}

// start records the subsession the agent asked for and answers with a
// made-up receipt; nothing runs.
func (c *Coord) start(req protocol.Request) (any, *protocol.Error) {
	p, perr := decode[protocol.ChildStartParams](req.Method, req.Params)
	if perr != nil {
		return nil, perr
	}
	invalid := func(message string) (any, *protocol.Error) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: message}
	}
	switch {
	case p.Harness == "":
		return invalid("harness is required")
	case strings.TrimSpace(p.Purpose) == "":
		return invalid("purpose is required: 3-8 words the dashboard shows")
	case strings.TrimSpace(p.Task) == "":
		return invalid("task is required: the subsession sees only this text")
	}
	if reason := c.refusal(); reason != "" {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: req.Method + ": " + reason}
	}
	if p.Mode == "" {
		p.Mode = "enhanced"
	}
	child := protocol.Child{
		RunID: fmt.Sprintf("%s-sub-%d", c.session.RunID, len(c.children)+1), ParentRunID: c.session.RunID, RootRunID: c.session.RunID,
		Purpose: p.Purpose, Harness: p.Harness, Mode: p.Mode, Model: p.Model, OwnerID: memberID, AccountMemberID: memberID,
		State: protocol.ChildStarting, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	c.children = append(c.children, child)
	return protocol.ChildReceipt{Child: child,
		Next: "The subsession's report arrives in your inbox and wakes you: end your turn or report waiting --for agents. Do not poll it."}, nil
}

func (c *Coord) report(req protocol.Request, n int) (any, *protocol.Error) {
	p, perr := decode[protocol.CoordReportParams](req.Method, req.Params)
	if perr != nil {
		return nil, perr
	}
	outcomes := []string{protocol.CoordOutcomeSuccess, protocol.CoordOutcomeFailure, protocol.CoordOutcomeBlocked,
		protocol.CoordOutcomeNeedsInput, protocol.CoordOutcomeWaiting, protocol.CoordOutcomeAnswered}
	if !slices.Contains(outcomes, p.Outcome) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("coord.report: outcome must be one of %s", strings.Join(outcomes, ", "))}
	}
	if strings.TrimSpace(p.Summary) == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "coord.report: summary is required"}
	}
	if unfinished := c.unfinished(); p.Outcome == protocol.CoordOutcomeSuccess && unfinished > 0 {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"coord.report: %d subsessions are unfinished (aether-internal subsession list); wait for their reports and report waiting --for agents, or stop them with subsession cancel", unfinished)}
	}
	return protocol.CoordReportResult{ReportID: fmt.Sprintf("report-eval-%d", n), Outcome: p.Outcome, Summary: p.Summary,
		NextAction: "end your turn; the next prompt or mail resumes you"}, nil
}
