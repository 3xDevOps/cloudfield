package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"math"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
	acp "github.com/coder/acp-go-sdk"
)

func (d *acpDriver) continuationOwed(entry *supervised) bool {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	return entry.accountSwap != nil && entry.accountSwap.Continuation != "" && !entry.accountSwap.Sending
}

// owedContinuation is the marker whose continuation the run's session is
// still owed, nil when there is none or it is a move's in flight, whose
// replacement is not this session.
func (d *acpDriver) owedContinuation(entry *supervised) *accountSwap {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if swap := entry.accountSwap; swap != nil && swap.Continuation != "" && !swap.Sending && !entry.moving {
		return swap
	}
	return nil
}

// sendContinuation sends swap's continuation once into the run's session,
// only while swap is still the run's marker and the conversation is idle: a
// conversation that went on since the move does not get it late. A prompt
// that never reached the agent leaves it owed.
func (d *acpDriver) sendContinuation(entry *supervised, swap *accountSwap) {
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	// A session the server cannot reach was sent nothing: the continuation
	// stays owed until the session comes up again.
	sess, err := d.live(ctx, entry.runID)
	if err != nil {
		slog.Warn("scheduler: reach the agent for its continuation", "run", entry.runID, "error", err)
		return
	}
	if !d.markSending(entry, swap) {
		return
	}
	started, err := sess.PromptIdle(ctx, []acp.ContentBlock{acp.TextBlock(swap.Continuation)}, nil)
	if errors.Is(err, acphost.ErrPromptUnsent) {
		d.unmarkSending(entry, swap)
		return
	}
	d.s.clearAccountSwap(entry, swap)
	// Mail held back for the continuation waits on no later turn end.
	d.wakeIdle(entry.runID)
	switch {
	case err == nil && !started:
		d.continuationSkipped(sess, entry, swap)
	case err == nil || usageLimited(err):
	case d.oneShot(entry):
		d.endOneShot(entry, false, agentstatus.Report{State: agentstatus.Idle, Reason: "send the continuation to the agent: " + err.Error()})
	default:
		slog.Warn("scheduler: send the continuation to the agent", "run", entry.runID, "error", err)
	}
}

// markSending records that swap's continuation is being sent, unless swap is
// no longer the run's marker, is sent already, or another move holds the
// run and would stop the agent it goes to.
func (d *acpDriver) markSending(entry *supervised, swap *accountSwap) bool {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry.accountSwap != swap || swap.Continuation == "" || swap.Sending || entry.moving {
		return false
	}
	swap.Sending = true
	// Durable before the send: a crash after it must not find the marker
	// from before the rename and send again.
	err := d.s.writeSidecar(entry.sidecar())
	if err == nil {
		err = fsyncDir(d.s.cfg.StateDir)
	}
	if err != nil {
		swap.Sending = false
		slog.Warn("scheduler: record the continuation as being sent; it stays owed", "run", entry.runID, "error", err)
		return false
	}
	return true
}

func (d *acpDriver) unmarkSending(entry *supervised, swap *accountSwap) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry.accountSwap != swap {
		return
	}
	swap.Sending = false
	if err := d.s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: record the continuation as owed again", "run", entry.runID, "error", err)
	}
}

// continued drops the continuation the run's session is owed once a member's
// prompt went on with the conversation, which it would reach late. One a
// move in flight will owe, or one being sent, stays.
func (d *acpDriver) continued(sess *acphost.Session, run domain.RunID) {
	d.s.mu.Lock()
	entry := d.s.runs[run]
	swap := (*accountSwap)(nil)
	if entry != nil {
		swap = entry.accountSwap
	}
	owed := swap != nil && swap.Continuation != "" && !swap.Sending && !entry.moving
	if owed {
		entry.accountSwap = nil
		if err := d.s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: clear the continuation the conversation went on without", "run", entry.runID, "error", err)
		}
	}
	d.s.mu.Unlock()
	if owed {
		d.continuationSkipped(sess, entry, swap)
	}
}

func (d *acpDriver) continuationSkipped(sess *acphost.Session, entry *supervised, swap *accountSwap) {
	ctx, cancel := context.WithTimeout(context.Background(), acpLoginTimeout)
	defer cancel()
	to := string(swap.To)
	if run, err := d.s.cfg.Store.GetRun(ctx, entry.runID); err == nil {
		to = d.s.accountLabel(ctx, swap.to(), run.Harness)
	}
	if err := sess.Notice("info", "Continuation prompt not sent", "The session moved to "+to+"; the continuation prompt was not sent because the conversation went on without it."); err != nil {
		slog.Warn("scheduler: record the skipped continuation", "run", entry.runID, "error", err)
	}
}

// endSent ends a Background run that a previous server process found
// sending its continuation and whose agent is in no turn, as its log shows:
// a continuation's turn that ended ends the run as a restart reads any
// turn's end, one cut short is resumed, and with no trace of it the run
// ends saying the continuation may not have arrived.
func (d *acpDriver) endSent(ctx context.Context, entry *supervised, sess *acphost.Session, run *domain.Run) {
	d.s.mu.Lock()
	swap := entry.accountSwap
	d.s.mu.Unlock()
	if swap == nil {
		return
	}
	sent := promptSeq(sess.Log(), swap.Continuation)
	switch {
	case sent == 0:
		reason := "the session moved to " + d.s.accountLabel(ctx, swap.to(), run.Harness) + "; the continuation prompt may not have been delivered"
		d.endOneShot(entry, false, agentstatus.Report{State: agentstatus.Idle, Reason: reason})
	case acphost.LastItem(sess.Log(), acphost.KindTurnEnd).Seq < sent:
		d.startOneShot(ctx, entry, sess, true, oneShotResume)
	default:
		d.startOneShot(ctx, entry, sess, false, oneShotResume)
	}
}

// promptSeq is the Seq of the latest prompt log records with text, 0 for
// none.
func promptSeq(log *acphost.Log, text string) int64 {
	for before := int64(math.MaxInt64); ; {
		items, err := log.ReadBefore(before, 512)
		if err != nil || len(items) == 0 {
			return 0
		}
		for i := len(items) - 1; i >= 0; i-- {
			if it := items[i]; it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == text {
				return it.Seq
			}
		}
		before = items[0].Seq
	}
}
