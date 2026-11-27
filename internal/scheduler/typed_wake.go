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

// Measured: when a dashboard click moves focus off the terminal, each agent
// CLI prints once within 30 ms and still submits a line pasted 30 ms later.
// One waiting on its model prints at least every 320 ms (Cursor; Copilot
// 105 ms, Gemini 65 ms).
const (
	typedWakeAskedQuiet = 750 * time.Millisecond
	typedWakeAskedWait  = 2 * time.Second
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

// Printing reports whether run's terminal printed too recently for a typed
// wake: its agent is working.
func (s *Scheduler) Printing(run domain.RunID) bool {
	out, _ := s.cfg.PTY.LastOutput(ptyhost.RunSession(run))
	return time.Since(out) < typedWakeQuietOutput
}

// typeWake types line into run's terminal and submits it as the agent's
// next prompt. It writes nothing and fails with domain.ErrWakeHeld while a
// person may be typing there, a request waits for an answer, the terminal is
// about to be replaced or it is not at rest: the line would join a draft,
// answer the request, be lost with the old terminal or stay unsent. The
// member in control who asks for the line (domain.WithWakeApproved) is not
// held for their own input, and their click's redraw is waited out: only an
// agent that keeps printing holds them. One who was shown the line and did
// not ask (domain.WithWakeShown) holds it with whatever they typed and did
// not press Enter after.
func (s *Scheduler) typeWake(ctx context.Context, run domain.RunID, line string) error {
	awake, err := s.holdAwakeNow(run)
	if err != nil {
		return err
	}
	defer awake()
	key := ptyhost.RunSession(run)
	asked := domain.WakeApproved(ctx)
	quiet, patience := typedWakeQuietOutput, time.Duration(0)
	if asked {
		quiet, patience = typedWakeAskedQuiet, typedWakeAskedWait
	}
	began := time.Now()
	for {
		out, _ := s.cfg.PTY.LastOutput(key)
		rest := quiet - time.Since(out)
		if rest <= 0 {
			break
		}
		if time.Since(began)+rest > patience {
			return fmt.Errorf("scheduler: %w: the terminal printed %s ago", domain.ErrWakeHeld, time.Since(out).Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("scheduler: typed wake: wait for the terminal to stop printing: %w", ctx.Err())
		case <-time.After(rest):
		}
	}
	s.mu.Lock()
	entry := s.runs[run]
	// A Standard session moving account is finalizing throughout. One
	// switching to Enhanced runs a login shell, with bracketed paste on,
	// until its mode is committed.
	if entry == nil || entry.paused || entry.finalizing || entry.killRequested || entry.atPrompt() || entry.switching != "" || entry.switchIntent != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: %w: the session is paused, ending, moving account, switching mode or waiting for an answer", domain.ErrWakeHeld)
	}
	unattended, agent, cid := entry.startedAt, entry.harness, entry.containerID
	if entry.quietFrom.After(unattended) {
		unattended = entry.quietFrom
	}
	s.mu.Unlock()
	// A restarted server knows neither who controlled the terminal nor when a person last typed in it.
	if in, _ := s.cfg.PTY.LastInput(key); in.After(unattended) && !asked {
		unattended = in
	}
	if time.Since(unattended) < typedWakeQuietInput {
		return fmt.Errorf("scheduler: %w: a person typed in the terminal, or the session started or resumed, in the last %s", domain.ErrWakeHeld, typedWakeQuietInput)
	}
	if domain.WakeShown(ctx) && !s.cfg.PTY.Drafting(key).IsZero() {
		return fmt.Errorf("scheduler: %w: the member in control typed in the terminal and has not pressed Enter since", domain.ErrWakeHeld)
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
	err = s.cfg.PTY.InjectPasted(ctx, key, "Cloudfield", "", strings.TrimSuffix(s.wakeNamed(ctx, run, line), "\n"), harness.SubmitSequence(agent))
	if errors.Is(err, ptyhost.ErrPasteModeOff) {
		return fmt.Errorf("scheduler: %w: %w", domain.ErrWakeHeld, err)
	}
	return err
}
