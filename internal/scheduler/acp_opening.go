package scheduler

import (
	"context"
	"fmt"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// opening is a launch's first open running after the launch returned, so the
// run takes prompts while its agent starts.
type opening struct {
	// done is closed once the open ended and everything held for it ran.
	done chan struct{}
	held []func()
}

func (d *acpDriver) Start(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		return err
	}
	if !d.continuationOwed(entry) {
		// A Background run takes no prompt, so nothing waits on its launch
		// returning early.
		if d.oneShot(entry) || !d.openBehindLaunch(ctx, entry) {
			d.connect(ctx, entry, openNew)
		}
		return nil
	}
	// The move that started this container reports a failed restore as its
	// own error, so it is recorded here rather than reported.
	if err := d.openRestored(ctx, entry, openContinue); err != nil {
		d.setRun(entry.runID, &acpRun{err: err})
		d.notice(entry.runID, "error", "Enhanced agent failed", err.Error())
	}
	return nil
}

// openBehindLaunch opens entry's first session without holding the launch,
// and reports whether it did: Close waits for the open, and cancels superCtx
// under s.mu, so none starts once it waits. It takes the run's op lock before
// the launch returns, so a stop, a mode switch or a resume right after it
// runs behind the open, and keeps the memory brake off the run's VM, as the
// launch did until it returned.
func (d *acpDriver) openBehindLaunch(ctx context.Context, entry *supervised) bool {
	d.s.mu.Lock()
	if d.s.superCtx.Err() != nil {
		d.s.mu.Unlock()
		return false
	}
	d.s.wg.Add(1)
	d.s.mu.Unlock()
	needed := d.needGuest(entry)
	unlock := d.lockOp(entry.runID)
	o := &opening{done: make(chan struct{})}
	d.mu.Lock()
	d.opening[entry.runID] = o
	d.mu.Unlock()
	go func() {
		defer d.s.wg.Done()
		defer needed()
		d.connectLocked(ctx, entry, openNew)
		runtime.Step(ctx, "agent_start")
		runtime.LogSteps(ctx, "scheduler: agent start", "run", entry.runID)
		unlock()
		for {
			d.mu.Lock()
			if len(o.held) == 0 {
				delete(d.opening, entry.runID)
				// A viewer that found no session waits for one, which a
				// failed open never gives it.
				d.wakeLocked(entry.runID)
				d.mu.Unlock()
				close(o.done)
				return
			}
			deliver := o.held[0]
			o.held = o.held[1:]
			d.mu.Unlock()
			deliver()
		}
	}()
	return true
}

// afterOpening runs f once the launch's open of run's first session has
// ended, at once when none is running.
func (d *acpDriver) afterOpening(run domain.RunID, f func()) {
	d.mu.Lock()
	o := d.opening[run]
	if o != nil {
		o.held = append(o.held, f)
	}
	d.mu.Unlock()
	if o == nil {
		f()
	}
}

// hold keeps prompt for the session the launch is still opening and reports
// whether it did. The prompt reaches the agent after the launch's task and
// the prompts held before it; delivered then gets what became of it.
func (d *acpDriver) hold(ctx context.Context, run *domain.Run, prompt domain.AgentPrompt, steer bool, delivered func(error)) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.opening[run.ID]
	if o == nil {
		return false
	}
	ctx = context.WithoutCancel(ctx)
	o.held = append(o.held, func() {
		d.s.mu.Lock()
		entry := d.s.runs[run.ID]
		moving := entry != nil && entry.moving
		d.s.mu.Unlock()
		sess, err, _ := d.liveNow(run.ID)
		var outcome string
		switch {
		case moving:
			err = d.s.movingError(ctx, run.ID)
		case err == nil:
			outcome, err = d.prompt(ctx, sess, run, prompt, steer, delivered)
		default:
			err = fmt.Errorf("%w: %w", ptyhost.ErrNoSession, err)
		}
		// A prompt queued behind a turn reports through prompt's callback.
		if delivered != nil && (err != nil || outcome != acphost.OutcomeQueued) {
			delivered(err)
		}
	})
	return true
}

// awaitOpening waits for the launch's open of run's first session, if one is
// running.
func (d *acpDriver) awaitOpening(ctx context.Context, run domain.RunID) error {
	d.mu.Lock()
	o := d.opening[run]
	d.mu.Unlock()
	if o == nil {
		return nil
	}
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the enhanced session: %w", ctx.Err())
	}
}

func (d *acpDriver) starting(run domain.RunID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opening[run] != nil
}

// liveStarted is live for a call that acts on what the agent is doing. A
// session still starting has no turn to cancel and no request to answer,
// and such a call holds the run's control admission, which must not wait
// for the start or the prompts held for it.
func (d *acpDriver) liveStarted(ctx context.Context, run domain.RunID) (*acphost.Session, error) {
	if !d.starting(run) {
		return d.live(ctx, run)
	}
	sess, err, opened := d.liveNow(run)
	if !opened {
		err = fmt.Errorf("%w: the agent is still starting", err)
	}
	return sess, err
}
