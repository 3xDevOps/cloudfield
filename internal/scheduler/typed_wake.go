package scheduler

import (
	"context"
	"errors"
	"fmt"
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
// records no idle wake.
func (s *Scheduler) TypedWake(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.launchMode != domain.LaunchTUI || !entry.idleWake {
		return false
	}
	profile, _ := harness.Lookup(entry.harness)
	return profile.Capabilities.TypedWake
}

// typeWake types line into run's terminal and submits it as the agent's
// next prompt. It writes nothing and fails while a person may be typing
// there, a request waits for an answer or the terminal is not at rest: the
// line would join a draft, answer the request or stay unsent.
func (s *Scheduler) typeWake(ctx context.Context, run domain.RunID, line string) error {
	awake, err := s.holdAwakeNow(run)
	if err != nil {
		return err
	}
	defer awake()
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil || entry.paused || entry.switching != "" || entry.atPrompt() {
		s.mu.Unlock()
		return errors.New("scheduler: typed wake held: the session is paused, switching mode or waiting for an answer")
	}
	quiet, agent := entry.startedAt, entry.harness
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
	return s.cfg.PTY.InjectPasted(ctx, key, "Cloudfield", "", line, harness.SubmitSequence(agent))
}
