package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	register(command{
		name:  "message",
		short: "send a message to a session's agent (- reads it from stdin)",
		usage: "cloudfield message [--steer] [--attach <path>]... <session-id> <message...>\n" +
			"cloudfield message [--steer] [--attach <path>]... <session-id> -\n" +
			"  flags go before <session-id>: every word after it is the message\n" +
			"  a --steer or --attach word after <session-id> is refused, not sent as text\n" +
			"  with --attach the message is optional",
		example: "cloudfield message run-d6ay38tj8j run the tests again\n" +
			"cloudfield message run-d6ay38tj8j -\n" +
			"cloudfield message --steer --attach shot.png run-d6ay38tj8j match this layout instead",
		flags: true,
		run:   runMessage,
	})
}

func runMessage(args []string) error {
	send, err := parseMessage(args, os.Stdin)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error { return send(c, os.Stdout) })
}

// parseMessage checks a message command's arguments and attachments before
// anything dials the server.
func parseMessage(args []string, stdin io.Reader) (func(*protocol.Client, io.Writer) error, error) {
	fs := newFlagSet("message")
	steer := fs.Bool("steer", false, "add the message to an Enhanced agent's running turn instead of queueing it behind that turn; "+
		"changes nothing in a Standard session, when no turn runs, when the agent cannot steer, or while the message waits for the member in control")
	var attach stringList
	fs.Var(&attach, "attach", "upload the file at `path` and send it with the message "+
		"(repeatable, at most 8; images up to 8 MiB, other files up to 64 MiB)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() == 0 || fs.NArg() == 1 && len(attach) == 0 {
		return nil, fmt.Errorf("usage: cloudfield message [--steer] [--attach <path>]... <session-id> <message...>|-")
	}
	for _, word := range fs.Args()[1:] {
		name, _, _ := strings.Cut(strings.TrimLeft(word, "-"), "=")
		// A quoted message is one argument with spaces in it; only a bare word can be a misplaced flag.
		if strings.HasPrefix(word, "-") && !strings.ContainsFunc(word, unicode.IsSpace) && fs.Lookup(name) != nil {
			return nil, fmt.Errorf("message: %s comes after the session id, where it would be sent as message text; "+
				"put flags before %s, or to send it as written quote the whole message, "+
				"or pipe a one-word message on stdin with -", word, fs.Arg(0))
		}
	}
	if len(attach) > protocol.CollaborationMaxAttachments {
		return nil, fmt.Errorf("message: %d files attached, at most %d", len(attach), protocol.CollaborationMaxAttachments)
	}
	for _, path := range attach {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("attach %s: not a regular file", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		// http.DetectContentType looks at no more than 512 bytes.
		head, err := io.ReadAll(io.LimitReader(f, 512))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		limit, what := int64(configMaxFileBytes), "64 MiB"
		if terminalImage(head) {
			limit, what = domain.MaxImageBytes, "8 MiB image"
		}
		if info.Size() > limit {
			return nil, fmt.Errorf("attach %s: %d bytes is over the %s limit", path, info.Size(), what)
		}
	}
	text, err := stdinText(strings.Join(fs.Args()[1:], " "), "message", stdin)
	if err != nil {
		return nil, err
	}
	return func(c *protocol.Client, w io.Writer) error {
		return sendMessage(c, w, fs.Arg(0), text, attach, *steer, cli.NewControlSessionID())
	}, nil
}

// terminal.image accepts only these four formats.
func terminalImage(data []byte) bool {
	return slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, http.DetectContentType(data))
}

func sendMessage(c *protocol.Client, w io.Writer, runID, text string, attach []string, steer bool, key string) error {
	params := protocol.RunInjectParams{RunID: runID, Message: text, IdempotencyKey: key, Steer: steer}
	for _, path := range attach {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := base64.StdEncoding.EncodeToString(data)
		var uploaded protocol.TerminalUploadResult
		if terminalImage(data) {
			err = c.Call(protocol.MethodTerminalImage, protocol.TerminalImageParams{RunID: runID, Content: content}, &uploaded)
		} else {
			err = c.Call(protocol.MethodTerminalFile, protocol.TerminalFileParams{RunID: runID, Name: filepath.Base(path), Content: content}, &uploaded)
		}
		if err != nil {
			return fmt.Errorf("attach %s: %w", path, runNotFound(runID, err))
		}
		params.Attachments = append(params.Attachments, uploaded.Path)
	}
	var res protocol.RunRoomPostResult
	if err := c.Call(protocol.MethodRunInject, params, &res); err != nil {
		return runNotFound(runID, err)
	}
	m := res.Message
	reason := ""
	if m.Failure != nil {
		reason = ": " + m.Failure.Message
	}
	var line string
	switch m.State {
	case protocol.RoomMessageSent:
		if m.AgentDelivery == "queued" {
			line = fmt.Sprintf("message %s sent to session %s, queued behind the agent's current turn", m.ID, runID)
			break
		}
		line = fmt.Sprintf("message %s delivered to session %s", m.ID, runID)
	case protocol.RoomMessageQueued:
		line = fmt.Sprintf("message %s queued for the member in control of session %s, who can approve or deny it", m.ID, runID)
		if m.DeliverAfter != nil {
			line += "; otherwise it is delivered at " + *m.DeliverAfter
		}
	case protocol.RoomMessageUncertain:
		line = fmt.Sprintf("message %s to session %s: delivery uncertain%s", m.ID, runID, reason)
	default:
		return fmt.Errorf("message %s to session %s was %s%s", m.ID, runID, strings.ReplaceAll(string(m.State), "_", " "), reason)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// stdinText returns value, or all of stdin when value is "-".
func stdinText(value, what string, stdin io.Reader) (string, error) {
	if value != "-" {
		return value, nil
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", what, err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", fmt.Errorf("%s on stdin is empty", what)
	}
	return text, nil
}
