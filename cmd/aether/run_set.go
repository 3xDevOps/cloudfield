package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

const runSetUsage = "usage: cloudfield session set <session-id> [--model <model>] [--effort <level>]"

func runSet(args []string) error {
	runID := args[0]
	fs := newFlagSet("session set")
	model := fs.String("model", "", "model to change to, one the session's agent offers")
	effort := fs.String("effort", "", "effort or thinking level to change to, one the session's agent offers")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if (*model == "" && *effort == "") || fs.NArg() != 0 {
		return errors.New(runSetUsage)
	}
	return withControl(func(c *protocol.Client) error {
		return setSessionOptions(c, os.Stdout, runID, *model, *effort)
	})
}

func setSessionOptions(c *protocol.Client, w io.Writer, runID, model, effort string) error {
	var res protocol.RunOptionSetResult
	if err := c.Call(protocol.MethodRunOptionSet, protocol.RunOptionSetParams{RunID: runID, Model: model, Effort: effort}, &res); err != nil {
		return runNotFound(runID, err)
	}
	// What the agent reports: it can take a value under another name.
	var set []string
	if model != "" {
		set = append(set, "model "+res.Model)
	}
	if effort != "" {
		set = append(set, "effort "+res.Effort)
	}
	_, err := fmt.Fprintf(w, "session %s set to %s\n", runID, strings.Join(set, ", "))
	return err
}
