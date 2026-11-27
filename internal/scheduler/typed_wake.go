package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
)

// Measured: a line typed within 1.5 s of terminal output can stay unsent, and a draft in the input box is submitted joined to it.
const (
	typedWakeQuietOutput = 3 * time.Second
	typedWakeQuietInput  = 10 * time.Second
)

// TypedWake reports a Standard session whose idle wake is a line typed into
// its terminal. A launch on a definition that replaces the shipped agent
// records no idle wake, and a session whose agent exited loses it.
func (s *Scheduler) TypedWake(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.launchMode != domain.LaunchTUI || !entry.idleWake || entry.retained {
		return false
	}
	profile, _ := harness.Lookup(entry.harness)
	return profile.Capabilities.TypedWake
}

// typeWake types line into run's terminal and submits it as the agent's
// next prompt. It writes nothing and fails while a person may be typing
// there, a request waits for an answer, the terminal is about to be replaced
// or it is not at rest: the line would join a draft, answer the request, be
// lost with the old terminal or stay unsent.
func (s *Scheduler) typeWake(ctx context.Context, run domain.RunID, line string) error {
	awake, err := s.holdAwakeNow(run)
	if err != nil {
		return err
	}
	defer awake()
	s.mu.Lock()
	entry := s.runs[run]
	// A Standard session moving account is finalizing throughout.
	if entry == nil || entry.paused || entry.finalizing || entry.killRequested || entry.atPrompt() {
		s.mu.Unlock()
		return errors.New("scheduler: typed wake held: the session is paused, ending, moving account or waiting for an answer")
	}
	quiet, agent, cid := entry.startedAt, entry.harness, entry.containerID
	if entry.quietFrom.After(quiet) {
		quiet = entry.quietFrom
	}
	s.mu.Unlock()
	key := ptyhost.RunSession(run)
	if out, _ := s.cfg.PTY.LastOutput(key); out.After(quiet) {
		quiet = out
	}
	if since := time.Since(quiet); since < typedWakeQuietOutput {
		return fmt.Errorf("scheduler: typed wake held: the terminal printed, started or resumed %s ago", since.Round(time.Millisecond))
	}
	if in, _ := s.cfg.PTY.LastInput(key); time.Since(in) < typedWakeQuietInput {
		return errors.New("scheduler: typed wake held: a person typed in the terminal")
	}
	// The supervisor leaves a login shell where the agent exited, and bash
	// turns bracketed paste on at its prompt.
	state, err := s.supervisorState(ctx, cid)
	if err != nil {
		return fmt.Errorf("scheduler: typed wake: read the session supervisor's state: %w", err)
	}
	if strings.Contains(state, "exited ") {
		s.mu.Lock()
		entry.idleWake = false
		s.mu.Unlock()
		return errors.New("scheduler: typed wake dropped: the agent exited and its terminal runs a shell")
	}
	return s.cfg.PTY.InjectPasted(ctx, key, "Cloudfield", "", strings.TrimSuffix(line, "\n"), harness.SubmitSequence(agent))
}
