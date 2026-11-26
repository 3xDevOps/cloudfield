package main

import (
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	register(command{
		name:    "interrupt",
		short:   "stop the agent's current turn",
		usage:   "cloudfield interrupt <session-id>",
		example: "cloudfield interrupt run-d6ay38tj8j",
		run:     runInterrupt,
	})
}

func runInterrupt(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: cloudfield interrupt <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return interruptSession(c, os.Stdout, args[0])
	})
}

func interruptSession(c *protocol.Client, w io.Writer, runID string) error {
	if err := c.Call(protocol.MethodRunInterrupt, protocol.RunIDParams{RunID: runID}, nil); err != nil {
		return runNotFound(runID, err)
	}
	_, err := fmt.Fprintf(w, "stopped the agent's turn in session %s\n", runID)
	return err
}
