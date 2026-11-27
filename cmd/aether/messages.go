package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	register(command{
		name:    "messages",
		short:   "read the messages agents sent each other (not the ones you send with cloudfield message)",
		usage:   "cloudfield messages [<session-id>] [--workspace <name>] [--thread <id>] [--limit <n>] [--before <cursor>] [--json]",
		example: "cloudfield messages run-d6ay38tj8j",
		flags:   true,
		run:     runMessages,
	})
}

func runMessages(args []string) error {
	call, err := parseMessages(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error { return call(c, os.Stdout, os.Stderr) })
}

func parseMessages(args []string) (func(c *protocol.Client, w, errOut io.Writer) error, error) {
	fs := newFlagSet("messages")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the session's, or the only workspace)")
	thread := fs.String("thread", "", "only this thread: a question and its replies, or one report")
	limit := fs.Int("limit", 50, "messages to show, at most 100")
	before := fs.String("before", "", "cursor an earlier call printed; shows the page of messages older than that call's")
	asJSON := fs.Bool("json", false, "print the server's rows, newest first, with next_before as the cursor")
	var run string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		run, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch {
	case fs.NArg() > 1, fs.NArg() == 1 && run != "":
		return nil, errors.New("usage: cloudfield messages [<session-id>] [flags]")
	case fs.NArg() == 1:
		run = fs.Arg(0)
	}
	return func(c *protocol.Client, w, errOut io.Writer) error {
		wsID, err := timelineWorkspace(c, *workspace, run)
		if err != nil {
			return err
		}
		var res protocol.CoordMessagesListResult
		if err = c.Call(protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{
			WorkspaceID: wsID, RunID: run, CorrelationID: *thread, Before: *before, Limit: *limit,
		}, &res); err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(w, res)
		}
		var out strings.Builder
		for _, m := range slices.Backward(res.Messages) {
			head := fmt.Sprintf("%s  %s -> %s  %s  ", shortTime(m.CreatedAt), m.FromRunID, m.ToRunID, m.Kind)
			switch {
			case m.AckedAt != nil:
				head += "acknowledged"
			case m.DeliveredAt != nil:
				head += "delivered"
			default:
				head += "sent"
			}
			if m.CorrelationID != "" {
				head += "  thread " + m.CorrelationID
			}
			out.WriteString(printable(head) + "\n")
			text := strings.Split(strings.TrimRight(m.Body, "\n"), "\n")
			if m.Outcome != "" {
				text = append(text, "outcome: "+m.Outcome)
			}
			if m.NextAction != "" {
				text = append(text, "next action: "+m.NextAction)
			}
			for _, line := range text {
				out.WriteString("  " + printable(line) + "\n")
			}
		}
		if out.Len() == 0 {
			out.WriteString("no agent messages\n")
		}
		if _, err = io.WriteString(w, out.String()); err != nil {
			return err
		}
		if res.NextBefore != "" {
			_, _ = fmt.Fprintln(errOut, "older messages: add --before "+res.NextBefore)
		}
		return nil
	}, nil
}
