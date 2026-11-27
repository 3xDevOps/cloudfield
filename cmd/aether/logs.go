package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	register(command{
		name:    "logs",
		short:   "print what a session's agent did as plain text: terminal lines, or an Enhanced session's conversation, live with --follow",
		usage:   "cloudfield logs <session-id> [--limit <n>] [--follow]",
		example: "cloudfield logs run-d6ay38tj8j --limit 50\ncloudfield logs run-d6ay38tj8j --follow",
		flags:   true,
		run:     runLogs,
	})
}

// logsFollowPoll is a variable so a test can shorten it.
var logsFollowPoll = 2 * time.Second

type acpOpen = func(protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error)

const thoughtHead = "thought: "

func runLogs(args []string) error {
	fs := newFlagSet("logs")
	limit := fs.Int("limit", 200, "print the newest N terminal lines, or the newest N lines of an Enhanced session's conversation")
	follow := fs.Bool("follow", false, "keep printing an Enhanced session's conversation as it is written, until the session ends")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: cloudfield logs <session-id> [--limit N] [--follow]", err)
	}
	if runID == "" || *limit < 1 {
		return fmt.Errorf("usage: cloudfield logs <session-id> [--limit N] [--follow]")
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var open acpOpen
	if *follow {
		open = conn.ACPStream
	}
	return olderServer(printLogs(c, os.Stdout, runID, *limit, open))
}

func printLogs(c *protocol.Client, w io.Writer, runID string, limit int, open acpOpen) error {
	r, err := getRun(c, runID)
	if err != nil {
		return err
	}
	switch {
	case r.ACP:
		return printACPLogs(c, w, r.ID, limit, open)
	case open != nil:
		return fmt.Errorf("session %s is a Standard session, which has no conversation to follow; cloudfield attach %s --read-only watches its terminal live", r.ID, r.ID)
	}
	return printTerminalLogs(c, w, r.ID, limit)
}

func printTerminalLogs(c *protocol.Client, w io.Writer, runID string, limit int) error {
	var pages [][]protocol.TerminalHistoryLine
	before, read, truncated := "", 0, false
	seen := map[string]bool{}
	for read < limit {
		var res protocol.TerminalHistoryResult
		if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{RunID: runID, Before: before,
			Limit: min(limit-read, protocol.MaxTerminalHistoryLimit)}, &res); err != nil {
			return err
		}
		pages, read = append(pages, res.Lines), read+len(res.Lines)
		if !res.HasMore || res.NextCursor == "" || seen[res.NextCursor] {
			truncated = res.TruncatedBefore
			break
		}
		seen[res.NextCursor], before = true, res.NextCursor
	}
	if truncated {
		if _, err := fmt.Fprintln(w, "[earlier output has expired]"); err != nil {
			return err
		}
	}
	for _, page := range slices.Backward(pages) {
		for _, line := range page {
			if _, err := fmt.Fprintln(w, printable(line.Text)); err != nil {
				return err
			}
		}
	}
	return nil
}

// acpEntry is one line of an Enhanced session's log: a message, a thought,
// a tool call, a plan, a request or a notice. A message is logged in segments
// and the others in snapshots, so an entry gathers its items by id within a
// turn (an agent may reuse a tool call id in a later turn) and sits where
// its first item was written.
type acpEntry struct {
	// head is a message's "role: " or a thought's thoughtHead, which its
	// segments append to. Any other entry has none, and its newest snapshot
	// replaces text.
	head string
	text strings.Builder
	// whole marks a message whose last segment arrived.
	whole bool
	// shown is what print has written of line.
	shown string
}

// acpItem is the part of a run's ACP item log aether logs reads. The client
// decodes it itself instead of importing the server's acphost package.
type acpItem struct {
	Turn     int64        `json:"turn"`
	Kind     string       `json:"kind"`
	Message  *acpMessage  `json:"message,omitempty"`
	ToolCall *acpToolCall `json:"tool_call,omitempty"`
	Plan     []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"plan"`
	PlanParentID string `json:"plan_parent_id"`
	Request      *struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"request"`
	Notice *struct {
		Severity    string `json:"severity"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"notice"`
}

type acpMessage struct {
	Role      string `json:"role"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
	Complete  bool   `json:"complete,omitempty"`
}

type acpToolCall struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

func (e *acpEntry) line() string {
	return printable(strings.TrimSpace(e.head + e.text.String()))
}

// acpLog is a session's entries and the message whose line print left open.
type acpLog struct {
	entries []*acpEntry
	byKey   map[string]*acpEntry
	open    *acpEntry
}

func acpMarker(text string) *acpEntry {
	e := &acpEntry{}
	e.text.WriteString(text)
	return e
}

func acpLogOf(frames []protocol.ACPFrame) *acpLog {
	l := &acpLog{}
	for _, f := range frames {
		l.add(f)
	}
	return l
}

// add files a frame under its entry and returns the entry, nil when the
// frame prints nothing. A reset drops every entry and returns a marker that
// is not one of them, so only a follow prints it.
func (l *acpLog) add(f protocol.ACPFrame) *acpEntry {
	var it acpItem
	if !f.Reset && json.Unmarshal(f.Item, &it) != nil {
		return nil
	}
	var id, head, text string
	switch {
	case f.Reset || it.Kind == "reset":
		l.entries, l.byKey = nil, nil
		return acpMarker("[conversation reset]")
	case (it.Kind == "message" || it.Kind == "thought") && it.Message != nil:
		id, head, text = it.Message.MessageID, it.Message.Role+": ", it.Message.Text
		if it.Kind == "thought" {
			head = thoughtHead
		}
	case it.Kind == "tool_call" && it.ToolCall != nil:
		id, text = it.ToolCall.ID, strings.TrimSpace("tool "+it.ToolCall.Status)+": "+it.ToolCall.Title
		if it.ToolCall.ExitCode != nil {
			text += fmt.Sprintf(" (exit %d)", *it.ToolCall.ExitCode)
		}
	case it.Kind == "plan" && len(it.Plan) > 0:
		steps := make([]string, len(it.Plan))
		for i, step := range it.Plan {
			steps[i] = step.Content + " (" + step.Status + ")"
		}
		id, text = it.PlanParentID, "plan: "+strings.Join(steps, "; ")
	case it.Kind == "request" && it.Request != nil:
		id, text = it.Request.ID, strings.TrimSpace("request "+it.Request.Status)+": "+it.Request.Title
	case it.Kind == "notice" && it.Notice != nil:
		// A notice is written once, so its seq keeps it an entry of its own.
		id, text = strconv.FormatInt(f.Seq, 10), "notice "+it.Notice.Severity+": "+it.Notice.Title
		if it.Notice.Description != "" {
			text += ": " + it.Notice.Description
		}
	default:
		return nil
	}
	key := fmt.Sprintf("%d %s %s", it.Turn, it.Kind, id)
	e := l.byKey[key]
	if e == nil {
		e = &acpEntry{head: head}
		if l.byKey == nil {
			l.byKey = map[string]*acpEntry{}
		}
		l.byKey[key] = e
		l.entries = append(l.entries, e)
	}
	if head == "" {
		e.text.Reset()
	}
	e.text.WriteString(text)
	e.whole = it.Message != nil && it.Message.Complete
	return e
}

// print writes what e gained since it was last printed. A message's line
// stays open while its segments arrive; another entry ends that line, and
// the message then resumes on a new one.
func (l *acpLog) print(w io.Writer, e *acpEntry) error {
	line := e.line()
	rest := line
	if e.head != "" {
		rest = strings.TrimPrefix(line, e.shown)
	}
	if e != l.open {
		if line == e.shown {
			return nil
		}
		if err := l.endLine(w); err != nil {
			return err
		}
		if e.head != "" && e.shown != "" {
			rest = e.head + strings.TrimLeft(rest, " ")
		}
	}
	e.shown, l.open = line, e
	if e.head == "" || e.whole {
		rest, l.open = rest+"\n", nil
	}
	_, err := io.WriteString(w, rest)
	return err
}

func (l *acpLog) endLine(w io.Writer) error {
	if l.open == nil {
		return nil
	}
	l.open = nil
	_, err := fmt.Fprintln(w)
	return err
}

// The server cuts a large frame; when the whole item cannot be fetched, a
// message's or tool call's cut text stays, marked with the fetch error.
func fullACPFrame(c *protocol.Client, runID string, f protocol.ACPFrame) protocol.ACPFrame {
	if !f.Truncated {
		return f
	}
	var res protocol.RunACPItemResult
	fetchErr := c.Call(protocol.MethodRunACPItem, protocol.RunACPItemParams{RunID: runID, Seq: f.Seq}, &res)
	if fetchErr == nil {
		f.Item = res.Item
		return f
	}
	mark := fmt.Sprintf(" [truncated; %s: %v]", protocol.MethodRunACPItem, fetchErr)
	var item map[string]json.RawMessage
	var it acpItem
	if json.Unmarshal(f.Item, &item) != nil || json.Unmarshal(f.Item, &it) != nil {
		return f
	}
	var err error
	switch {
	case it.Message != nil:
		it.Message.Text += mark
		item["message"], err = json.Marshal(it.Message)
	case it.ToolCall != nil:
		it.ToolCall.Title += mark
		item["tool_call"], err = json.Marshal(it.ToolCall)
	}
	if err != nil {
		return f
	}
	if raw, merr := json.Marshal(item); merr == nil {
		f.Item = raw
	}
	return f
}

// A reset drops every entry before it, so older pages add nothing.
func hasReset(frames []protocol.ACPFrame) bool {
	return slices.ContainsFunc(frames, func(f protocol.ACPFrame) bool {
		var it acpItem
		return f.Reset || json.Unmarshal(f.Item, &it) == nil && it.Kind == "reset"
	})
}

// One agent resumes a message or thought under its MessageID only after
// entries of other kinds, so the one whose first segment is off the page is
// the page's first message or its first thought.
// shortcut: subagents writing at once interleave their messages, so one of
// theirs later on the page can start off it too. No item marks a first
// segment; read back to the turn's start if that cut line has to go.
func enough(entries []*acpEntry, limit int) bool {
	first := max(
		slices.IndexFunc(entries, func(e *acpEntry) bool { return e.head == thoughtHead }),
		slices.IndexFunc(entries, func(e *acpEntry) bool { return e.head != "" && e.head != thoughtHead }))
	return len(entries) > limit && len(entries)-limit > first
}

// printACPLogs reads pages back from the newest until the last limit entries
// are whole, prints them, and with open set follows the session.
func printACPLogs(c *protocol.Client, w io.Writer, runID string, limit int, open acpOpen) error {
	var frames []protocol.ACPFrame
	var before int64
	truncated := false
	for {
		var res protocol.RunACPHistoryResult
		if err := c.Call(protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: runID, BeforeSeq: before}, &res); err != nil {
			return err
		}
		for i, f := range res.Frames {
			res.Frames[i] = fullACPFrame(c, runID, f)
		}
		frames, truncated = append(res.Frames, frames...), res.TruncatedBefore
		if len(res.Frames) == 0 || res.Frames[0].Seq <= res.OldestSeq || hasReset(res.Frames) || enough(acpLogOf(frames).entries, limit) {
			break
		}
		before = res.Frames[0].Seq
	}
	l := acpLogOf(frames)
	entries := l.entries
	if len(entries) > limit {
		entries, truncated = entries[len(entries)-limit:], false
	}
	if truncated {
		if _, err := fmt.Fprintln(w, "[earlier conversation has expired]"); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := l.print(w, e); err != nil {
			return err
		}
	}
	var err error
	if open != nil {
		var last int64
		if len(frames) > 0 {
			last = frames[len(frames)-1].Seq
		}
		err = l.follow(c, w, runID, last, open)
	}
	return errors.Join(err, l.endLine(w))
}

// follow prints what the session writes after last until the session ends.
// The server ends a stream when the agent session ends, when this reader
// falls too far behind, and when an agent session starts on a stream opened
// with none; each is answered by subscribing again after the last item read.
// A stream opened with no agent session carries nothing past its replay, so
// the status of a session that has not ended is read beside its stream, and
// a session that has ended is read to the end of its replay and no further.
func (l *acpLog) follow(c *protocol.Client, w io.Writer, runID string, last int64, open acpOpen) error {
	for {
		r, err := getRun(c, runID)
		if err != nil {
			return err
		}
		ended := domain.RunStatus(r.Status).Terminal()
		stream, ack, err := open(protocol.ACPStreamRequest{RunID: runID, AfterSeq: last})
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer func() { _ = stream.Close() }()
			for !ended {
				select {
				case <-done:
					return
				case <-time.After(logsFollowPoll):
				}
				if now, gerr := getRun(c, runID); gerr != nil || domain.RunStatus(now.Status).Terminal() {
					return
				}
			}
			<-done
		}()
		br := bufio.NewReader(stream)
		var werr error
		for owed := ack.Replay; werr == nil && (!ended || owed > 0); {
			line, rerr := protocol.ReadLine(br)
			if rerr != nil {
				break
			}
			var f protocol.ACPFrame
			if json.Unmarshal(line, &f) != nil || f.Item == nil && !f.Reset {
				continue
			}
			if f.Item != nil {
				owed--
				// A replay holds only the newest items written after last.
				if last > 0 && f.Seq > last+1 {
					werr = l.print(w, acpMarker("[conversation skipped]"))
				}
			}
			last = f.Seq
			if e := l.add(fullACPFrame(c, runID, f)); e != nil && werr == nil {
				werr = l.print(w, e)
			}
		}
		close(done)
		if werr != nil || ended {
			return werr
		}
		time.Sleep(logsFollowPoll)
	}
}
