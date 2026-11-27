package scheduler

import (
	"context"
	"fmt"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// opening is a launch's first open running after the launch returned, so the
// run takes prompts while its agent starts.
type opening struct {
	// done is closed once the open ended and every prompt held for it was
	// delivered or refused.
	done chan struct{}
	held []func()
}

// openBehindLaunch opens entry's first session without holding the launch,
// and reports whether it did: Close waits for the open, and cancels superCtx
// under s.mu, so none starts once it waits. It takes the run's op lock before
// the launch returns, so a stop, a mode switch or a resume right after it
// runs behind the open.
func (d *acpDriver) openBehindLaunch(ctx context.Context, entry *supervised) bool {
	d.s.mu.Lock()
	if d.s.superCtx.Err() != nil {
		d.s.mu.Unlock()
		return false
	}
	d.s.wg.Add(1)
	d.s.mu.Unlock()
	unlock := d.lockOp(entry.runID)
	o := &opening{done: make(chan struct{})}
	d.mu.Lock()
	d.opening[entry.runID] = o
	d.mu.Unlock()
	go func() {
		defer d.s.wg.Done()
		d.connectLocked(ctx, entry, openNew)
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
		sess, err, _ := d.liveNow(run.ID)
		var outcome string
		if err == nil {
			outcome, err = d.prompt(ctx, sess, run, prompt, steer, delivered)
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
