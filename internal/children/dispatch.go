package children

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/linear"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// dispatch starts queued children while their parent has free slots, the
// oldest request first.
func (s *Service) dispatch(ctx context.Context) error {
	queued, err := s.cfg.Store.ChildrenInState(ctx, domain.ChildQueued)
	if err != nil {
		return err
	}
	parents := map[domain.RunID][]*domain.ChildReservation{}
	var order []domain.RunID
	for _, c := range queued {
		if parents[c.ParentRunID] == nil {
			order = append(order, c.ParentRunID)
		}
		parents[c.ParentRunID] = append(parents[c.ParentRunID], c)
	}
	for _, parent := range order {
		list := parents[parent]
		limits, err := s.cfg.Store.WorkspaceChildLimits(ctx, list[0].WorkspaceID)
		if err != nil {
			return err
		}
		counts, err := s.cfg.Store.ChildCounts(ctx, parent)
		if err != nil {
			return err
		}
		for free := limits.MaxLive - counts.Busy; free > 0 && len(list) > 0; free-- {
			c := list[0]
			// The launch that queued it again has yet to end, or it waits
			// for room and its attempt is not due. A younger one of its
			// parent does not start ahead of it.
			if s.held(c.RunID) || c.Error != "" && !s.retryDue() {
				break
			}
			list = list[1:]
			s.setInFlight(c.RunID, true)
			// A subsession tried again keeps the refusal it waits on in sight.
			if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildLaunching, c.Error, domain.ChildQueued); err != nil {
				s.setInFlight(c.RunID, false)
				if c.Error != "" {
					s.retryNow()
				}
				if errors.Is(err, store.ErrConflict) {
					free++
					continue
				}
				return err
			}
			go s.launch(ctx, c)
			// Its younger siblings wait for how its attempt ends.
			if c.Error != "" {
				break
			}
		}
	}
	return nil
}

// launch re-admits a reserved child exactly as a run launch is admitted,
// then hands it to the scheduler under its reserved ID.
func (s *Service) launch(ctx context.Context, c *domain.ChildReservation) {
	defer s.setInFlight(c.RunID, false)
	// An attempt that ended any other way than waiting again did not show
	// the server full: the next one waiting is tried without the delay.
	waits := false
	defer func() {
		if c.Error != "" && !waits {
			s.retryNow()
		}
	}()
	refuse := func(reason string) {
		if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildRefused, reason, domain.ChildLaunching); err != nil {
			slog.Warn("subsessions: refuse subsession", "child", c.RunID, "error", err)
			return
		}
		s.countWaitEnd(ctx, c, "refused", reason)
		s.publish(ctx, c.ParentRunID, "")
		// A child that never started has no run to send from.
		_ = s.notify(ctx, c.ParentRunID, c.ParentRunID,
			fmt.Sprintf("Subsession %s (%s) did not start: %s", c.RunID, c.Purpose, reason), "child-refused:"+string(c.RunID))
	}
	p, err := s.resolve(ctx, c.ParentRunID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		_ = s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildCancelled, "the parent session was deleted", domain.ChildLaunching)
		s.countWaitEnd(ctx, c, "cancelled", "the parent session was deleted")
		return
	case err != nil:
		refuse(err.Error())
		return
	case p.parent.Status.Terminal() && !c.Detached:
		_ = s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildCancelled, "the parent session ended before it started", domain.ChildLaunching)
		s.countWaitEnd(ctx, c, "cancelled", "the parent session ended before it started")
		return
	case p.refusal != "":
		refuse(p.refusal)
		return
	case p.owner != c.OwnerID:
		refuse(fmt.Sprintf("the parent session now belongs to %s, not %s, who asked for it", p.owner, c.OwnerID))
		return
	case p.settings.Off.Covers(c.Harness, c.AccountID):
		refuse(fmt.Sprintf("%s on account %s was turned off for subsessions after this one was queued", c.Harness, c.AccountID))
		return
	}
	if s.cfg.Cost != nil {
		if cost := s.cfg.Cost(); cost != nil {
			if admitErr := cost.Admit(ctx, c.WorkspaceID, c.OwnerID); admitErr != nil {
				refuse(admitErr.Error())
				return
			}
		}
	}
	measured, err := fallback.Measure(ctx, s.cfg.Store, s.cfg.Usage, c.OwnerID, string(c.AccountID), c.Harness, c.Fallback)
	if err != nil {
		refuse(err.Error())
		return
	}
	s.cfg.AuthorizationMu.Lock()
	unlock := sync.OnceFunc(s.cfg.AuthorizationMu.Unlock)
	defer unlock()
	// A handoff takes this lock, so the parent read here is the one the
	// launch runs under.
	parent, getErr := s.cfg.Store.GetRun(ctx, c.ParentRunID)
	if getErr != nil || parent.MemberID != c.OwnerID || parent.Status.Terminal() {
		unlock()
		refuse("the parent session ended or changed owner before this subsession started")
		return
	}
	workspaces := p.workspaces()
	requested, err := s.cfg.Authorize(ctx, s.cfg.Store, c.OwnerID, string(c.AccountID), workspaces)
	if err != nil {
		unlock()
		refuse(err.Error())
		return
	}
	chosen, excluded := fallback.Select(ctx, s.cfg.Store, measured, c.OwnerID, domain.AccountOf(requested), workspaces, c.Fallback, func(ctx context.Context, account domain.Account) (bool, error) {
		if p.settings.Off.Covers(c.Harness, account.Member) {
			return false, nil
		}
		return s.cfg.Runs.FallbackLaunchable(ctx, c.OwnerID, requested, account.Member, c.Harness, c.Mode)
	})
	account := chosen.Member
	if excluded != nil {
		slog.Warn("subsessions: fallback accounts left out of a subsession launch", "child", c.RunID, "error", excluded)
	}
	created := false
	_, err = s.cfg.Runs.LaunchWithOptions(ctx, c.WorkspaceID, c.OwnerID, account, c.Task, c.Harness, c.Mode, domain.LaunchOptions{
		Model: c.Model, Permission: parent.Permission, RequestedAccount: requested, Fallback: c.Fallback, ReservedRunID: c.RunID,
		Retry:      c.Error != "",
		FromCommit: c.BaseCommit, FromBranch: c.BaseBranch, ExtraWorkspaces: workspaces[1:], Admitted: unlock,
		LinearIssueURL: cmp.Or(linear.FindIssueURL(c.Task), parent.LinearIssueURL),
		Created: func(domain.RunID) {
			created = true
			if stateErr := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildStarted, "", domain.ChildLaunching); stateErr != nil {
				slog.Warn("subsessions: mark subsession started", "child", c.RunID, "error", stateErr)
			}
		},
	})
	if err != nil && !created {
		// Queued again before the lock goes, so a cancel behind it finds
		// the subsession in the queue.
		waits = s.cfg.Runs.CapacityRefused(err) && s.cfg.Now().Sub(c.CreatedAt) < capacityWaitLimit && s.wait(ctx, c, err.Error())
		unlock()
		if waits {
			s.toldWaiting(ctx, c, err.Error())
			return
		}
		refuse(err.Error())
		return
	}
	s.countWaitEnd(ctx, c, "started", "")
	s.Kick()
}

const (
	// capacityRetryEvery is the least time between two attempts to start
	// any subsession the server had no room for: each attempt may reclaim
	// disk or save idle VMs under the launch lock.
	capacityRetryEvery = 30 * time.Second
	// capacityWaitLimit is how long after it was asked for a subsession
	// still waits for room; after it a capacity refusal is final.
	capacityWaitLimit = 24 * time.Hour
)

// wait returns a subsession the server had no room for to the queue, to be
// tried again after capacityRetryEvery, and reports whether it did.
func (s *Service) wait(ctx context.Context, c *domain.ChildReservation, reason string) bool {
	s.mu.Lock()
	s.retryAt = s.cfg.Now().Add(capacityRetryEvery)
	s.mu.Unlock()
	if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildQueued, reason, domain.ChildLaunching); err != nil {
		slog.Warn("subsessions: queue a subsession the server had no room for", "child", c.RunID, "error", err)
		return false
	}
	slog.Info("subsessions: no room to start a subsession; it waits", "child", c.RunID, "parent", c.ParentRunID, "reason", reason)
	if c.Error == "" {
		s.count(ctx, domain.CapacityEvent{Kind: domain.CapacityWait, Op: waitOp, Run: c.RunID, Text: reason})
	}
	return true
}

// waitOp names subsessions in the capacity counters.
const waitOp = "subsession"

// countWaitEnd counts how the wait for room of c ended, if it waited, with
// the time since it was asked for.
func (s *Service) countWaitEnd(ctx context.Context, c *domain.ChildReservation, cause, text string) {
	if c.Error == "" {
		return
	}
	s.count(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitOp, Cause: cause, Run: c.RunID, Text: text, Waited: s.cfg.Now().Sub(c.CreatedAt)})
}

func (s *Service) count(ctx context.Context, e domain.CapacityEvent) {
	e.At = s.cfg.Now()
	if err := s.cfg.Store.CountCapacityEvent(ctx, e); err != nil {
		slog.Warn("subsessions: count a wait for room", "child", e.Run, "error", err)
	}
}

// toldWaiting tells subscribers a subsession waits for room, and its parent
// the first time it does.
func (s *Service) toldWaiting(ctx context.Context, c *domain.ChildReservation, reason string) {
	s.publish(ctx, c.ParentRunID, "")
	key := "child-waiting:" + string(c.RunID)
	if _, err := s.cfg.Store.GetRunMessageByIdempotency(ctx, c.ParentRunID, key); !errors.Is(err, store.ErrNotFound) {
		return
	}
	// A cancel may have taken it from the queue since the launch lock went.
	if now, err := s.cfg.Store.GetChild(ctx, c.RunID); err != nil || now.State == domain.ChildCancelled {
		return
	}
	_ = s.notify(ctx, c.ParentRunID, c.ParentRunID,
		fmt.Sprintf("Subsession %s (%s) is waiting to start: %s\nIt starts by itself when the server has room, and is refused if it has none within 24 hours of your request. To stop waiting: aether-internal subsession cancel --run-id %s",
			c.RunID, c.Purpose, reason, c.RunID), key)
}

// retryDue reports whether a subsession that waits for room may be tried
// now, and if so puts the next attempt capacityRetryEvery away.
func (s *Service) retryDue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if now.Before(s.retryAt) {
		return false
	}
	s.retryAt = now.Add(capacityRetryEvery)
	return true
}

// retryNow lets the next subsession that waits for room be tried at once.
func (s *Service) retryNow() {
	s.mu.Lock()
	s.retryAt = time.Time{}
	s.mu.Unlock()
}

// held reports whether a launch goroutine holds run's reservation.
func (s *Service) held(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[run]
}

// setInFlight records whether a launch goroutine holds run's reservation.
func (s *Service) setInFlight(run domain.RunID, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.inFlight[run] = true
	} else {
		delete(s.inFlight, run)
	}
}

// maxNoticeSummary bounds the report summary in a parent's notice, which a
// message cuts at 4 KiB, so the child's branch after it survives.
const maxNoticeSummary = 3000

// settle ends the attached children of ended parents and tells a parent
// once when a child ended without a final report. A parent a person closed
// takes its children with it: finished ones are closed as it was, live ones
// are stopped and closed. A completed parent not yet accepted keeps
// children that already reported, to close with it; any other ended
// parent's children are stopped.
func (s *Service) settle(ctx context.Context) error {
	list, err := s.cfg.Store.ChildrenToSettle(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		parent, err := s.cfg.Store.GetRun(ctx, c.ParentRunID)
		gone := errors.Is(err, store.ErrNotFound)
		if err != nil && !gone {
			return err
		}
		child, childErr := s.cfg.Store.GetRun(ctx, c.RunID)
		childLive := childErr == nil && !child.Status.Terminal()
		parentEnded := gone || parent.Status.Terminal()
		// A person closed the parent when they chose its final disposition
		// or accepted its completion; a kill also ends a run abandoned, and
		// its children are stopped below.
		closed := !gone && (parent.Status == domain.RunMerged ||
			(parent.Status == domain.RunAbandoned && parent.Reason != "killed") ||
			(parent.Status == domain.RunCompleted && parent.Review == domain.ReviewAccepted))
		if !c.Detached && parentEnded && closed && childErr == nil {
			// A child still provisioning cannot be closed; stop it first.
			if child.Status == domain.RunQueued || child.Status == domain.RunProvisioning {
				if stopErr := s.stop(ctx, c); stopErr != nil {
					slog.Warn("subsessions: stop the subsession of a closed session", "child", c.RunID, "parent", c.ParentRunID, "error", stopErr)
				}
				continue
			}
			// A child already in a final state has nothing to close.
			if child.Status.Final() {
				if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
					return err
				}
				continue
			}
			// The child's result was delivered to the parent and is part of
			// its work: close it as the parent was.
			var closeErr error
			if parent.Status == domain.RunCompleted {
				_, closeErr = s.cfg.Runs.Review(ctx, c.RunID, c.OwnerID, protocol.RunReviewComplete, false)
			} else {
				closeErr = s.cfg.Runs.CloseRun(ctx, c.RunID, c.OwnerID, parent.Status)
			}
			if closeErr != nil {
				slog.Warn("subsessions: close the subsession of a closed session", "child", c.RunID, "parent", c.ParentRunID, "error", closeErr)
				continue
			}
			if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
				return err
			}
			s.publish(ctx, c.RunID, "")
			continue
		}
		if !c.Detached && parentEnded && (c.State == domain.ChildQueued || childLive) {
			// A child that already delivered its result stays with a parent
			// someone can still close, whose state is what a person sees, and
			// closes with it. One whose parent is final or deleted is stopped.
			reported, err := s.cfg.Store.ChildReported(ctx, c.RunID)
			if err != nil {
				return err
			}
			if childLive && !gone && !parent.Status.Final() && (reported || finished(child)) {
				continue
			}
			if stopErr := s.stop(ctx, c); stopErr != nil {
				slog.Warn("subsessions: stop the subsession of an ended session", "child", c.RunID, "parent", c.ParentRunID, "error", stopErr)
			}
			continue
		}
		if childErr != nil {
			return childErr
		}
		if c.Detached || gone || c.EndNotified {
			if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.cfg.Store.LatestCoordReport(ctx, c.RunID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		} else if err != nil {
			// The notice's key makes a resend after a failed mark a no-op.
			msg := fmt.Sprintf("Subsession %s (%s) ended %s without a final report", c.RunID, c.Purpose, child.Status)
			if child.Reason != "" {
				msg += ": " + child.Reason
			}
			if latest, err := s.cfg.Store.LatestReportOfAnyOutcome(ctx, c.RunID); err == nil {
				msg += fmt.Sprintf("\nIts latest report, %s at %s: %s", latest.Outcome, latest.CreatedAt.UTC().Format(time.RFC3339), truncateSummary(latest.Summary))
			}
			if child.Branch != "" {
				msg += "\nIts branch: " + child.Branch
			}
			// A notice saved under this key with an older body still told the parent.
			if err := s.notify(ctx, c.RunID, c.ParentRunID, msg, "child-ended:"+string(c.RunID)); err != nil && !errors.Is(err, store.ErrIdempotencyConflict) {
				continue
			}
		}
		if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
			return err
		}
	}
	return nil
}

func finished(r *domain.Run) bool {
	switch r.Activity {
	case domain.ActivityFinished, domain.ActivityAnswered, domain.ActivityFailed:
		return true
	}
	return false
}

// idleNoticeAfter is how long a child may sit quiet before its parent is
// told.
const idleNoticeAfter = time.Hour

// stoppedNoticeAfter is how long a child stays stopped before its parent is
// told, so Aether's own restart of a refused or lost agent can settle first.
const stoppedNoticeAfter = time.Minute

// noticeIdle tells each parent once per quiet spell about an attached child
// that has waited, answered or asked its owner for over idleNoticeAfter.
func (s *Service) noticeIdle(ctx context.Context) error {
	return s.noticeSpells(ctx, "child-idle:", idleNoticeAfter, domain.RunActivity.Quiet, func(c store.ChildSpell, now time.Time) string {
		return fmt.Sprintf("Subsession %s (%s) has been %s for %s.", c.Child.RunID, c.Child.Purpose, c.Run.Activity, now.Sub(c.Since).Round(time.Minute)) +
			"\nCheck what it is doing, then wake it with an instruction: aether-internal send --to " + string(c.Child.RunID) +
			" --body <text>, or stop it: aether-internal subsession cancel --run-id " + string(c.Child.RunID)
	})
}

// stopped reports whether a live run in this activity and reason stopped on
// an error with nothing it reported saying so: its agent is not running, a
// turn failed or was cut short, a Standard agent's usage limit Aether did not
// act on included, or its wait's wake was never delivered.
func stopped(activity domain.RunActivity, reason string) bool {
	switch activity {
	case domain.ActivityDisconnected:
		return true
	case domain.ActivityFailed:
		return !strings.HasPrefix(reason, domain.ReportedFailureReason)
	case domain.ActivityNeedsYou:
		return reason == domain.InterruptedReason || strings.HasPrefix(reason, domain.StoppedShortReasonPrefix) ||
			strings.HasPrefix(reason, domain.UsageLimitReasonPrefix) || strings.HasPrefix(reason, domain.WaitBrokenReasonPrefix) ||
			agentstatus.IsUsageLimitMessage(reason)
	}
	return false
}

// noticeStopped tells each parent once per stopped spell about an attached
// child that stopped for over stoppedNoticeAfter.
func (s *Service) noticeStopped(ctx context.Context) error {
	return s.noticeSpells(ctx, "child-stopped:", stoppedNoticeAfter, stopped, func(c store.ChildSpell, _ time.Time) string {
		id := string(c.Child.RunID)
		msg := fmt.Sprintf("Subsession %s (%s) stopped (%s) and does not continue on its own.", id, c.Child.Purpose, c.Run.Activity)
		if c.Run.Activity == domain.ActivityDisconnected {
			msg += "\nIts agent is not running, so messages wait unread until a person reopens it with Retry Enhanced in its Chat view."
		} else {
			msg += "\nOnce the cause below is fixed, wake it with an instruction: aether-internal send --to " + id + " --body <text>."
		}
		return msg + "\nTo go on without it: aether-internal subsession cancel --run-id " + id
	})
}

// noticeSpells tells each parent once per spell about an attached child
// whose states have satisfied in for longer than after; head writes the
// first lines of the message. The notice's key holds the spell's start, so
// new work starts a new spell. A run a person paused, or whose result already
// reached its parent, is left alone.
func (s *Service) noticeSpells(ctx context.Context, prefix string, after time.Duration, in func(domain.RunActivity, string) bool, head func(store.ChildSpell, time.Time) string) error {
	now := s.cfg.Now()
	spells, err := s.cfg.Store.ChildrenInSpell(ctx, now.Add(-after), in)
	if err != nil {
		return err
	}
	for _, c := range spells {
		if s.cfg.Runs.Paused(c.Child.RunID) {
			continue
		}
		if reported, err := s.cfg.Store.ChildReported(ctx, c.Child.RunID); err != nil {
			return err
		} else if reported {
			continue
		}
		// An idle body names a growing duration, so a resend under the same
		// key would be refused as a different message.
		key := prefix + string(c.Child.RunID) + ":" + c.Since.UTC().Format(time.RFC3339Nano)
		if _, err := s.cfg.Store.GetRunMessageByIdempotency(ctx, c.Child.RunID, key); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// The commands come before the reason and report, which a long
		// question can push past the mailbox's body limit.
		msg := head(c, now)
		if c.Run.Reason != "" {
			msg += "\nIts status reason: " + truncateSummary(c.Run.Reason)
		}
		if latest, err := s.cfg.Store.LatestReportOfAnyOutcome(ctx, c.Child.RunID); err == nil {
			msg += fmt.Sprintf("\nIts latest report, %s at %s: %s", latest.Outcome, latest.CreatedAt.UTC().Format(time.RFC3339), truncateSummary(latest.Summary))
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_ = s.notify(ctx, c.Child.RunID, c.Child.ParentRunID, msg, key)
	}
	return nil
}

func truncateSummary(summary string) string {
	if len(summary) > maxNoticeSummary {
		return strings.ToValidUTF8(summary[:maxNoticeSummary], "") + "…"
	}
	return summary
}
