package collab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/cloudfield/internal/control"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// ErrWakeNotTyped means the member in control asked for a held mail wake
// now and its terminal could not take the line; the notice stays queued.
var ErrWakeNotTyped = errors.New("collab: the line was not typed and stays queued")

// Waker types the mail wake a notice held for run into its terminal. It
// reports false when nothing is left to wake, and fails, having typed
// nothing, while the terminal cannot take the line.
type Waker func(ctx context.Context, run domain.RunID) (bool, error)

// SetWaker installs what delivers a mail wake notice; until then a notice
// stays queued.
func (s *Service) SetWaker(w Waker) {
	s.mu.Lock()
	s.waker = w
	s.mu.Unlock()
}

// HoldWake shows the member connected in control of runID the mail wake the
// server owes its agent: a steer request under domain.ViaAgentMail that the
// run's owner authors, held for SteeringDelay like a teammate's message, and
// delivered by the Waker instead of as its own text. One key makes one
// notice. held reports a notice that stands undecided, and settled that the
// notice for key was typed or dismissed, which a restart does not forget.
// With neither, the wake is the caller's to type: nobody is connected in
// control, or run is a subsession, whose mail waits for the member to let go
// as its parent is told. A busy run admission fails with
// control.ErrAdmissionBusy instead of being waited for.
func (s *Service) HoldWake(ctx context.Context, runID domain.RunID, key, notice string) (held, settled bool, _ error) {
	notices, ok := s.cfg.Store.(interface {
		store.OpenRoomRequestStore
		GetRoomMessageByIdempotency(context.Context, domain.MemberID, domain.RunID, string) (*store.RoomMessage, error)
	})
	if !ok || s.cfg.Control == nil {
		return false, false, nil
	}
	var created *store.RoomMessage
	// The caller holds a coordination reference to the run, which cleanup
	// inside this admission waits for.
	err := s.cfg.Control.TryAdmitSnapshot(string(runID), func(current control.Snapshot, controlled bool) error {
		requests, err := notices.ListOpenRoomRequests(ctx, runID)
		if err != nil {
			return err
		}
		// A notice still held covers the mail that arrived after it.
		for _, m := range requests {
			if m.Via == domain.ViaAgentMail {
				held = true
				return nil
			}
		}
		run, err := s.cfg.Runs.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		for {
			prior, lookupErr := notices.GetRoomMessageByIdempotency(ctx, run.MemberID, runID, key)
			if errors.Is(lookupErr, store.ErrNotFound) {
				break
			}
			if lookupErr != nil {
				return lookupErr
			}
			if prior.State != store.RoomMessageCancelled && prior.State != store.RoomMessageNotSent {
				settled = prior.State == store.RoomMessageDenied || prior.State == store.RoomMessageSent
				held = !settled
				return nil
			}
			// Protection cancelled that notice, or its session took no
			// typed wake when it fell due: the mail is owed another.
			key += "-" + prior.ID
		}
		if run.Protected || !controlled || !current.Connected || run.ParentRunID != "" && !run.Detached {
			return nil
		}
		due := s.now().Add(SteeringDelay)
		created = &store.RoomMessage{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, ActorID: run.MemberID, Kind: store.RoomMessageSteerRequest,
			Body: notice, IdempotencyKey: key, Via: domain.ViaAgentMail, DeliverAfter: &due,
		}
		held = true
		return s.cfg.Store.CreateRoomMessage(ctx, created)
	})
	if err == nil && created != nil {
		err = s.publishMessage(ctx, created)
	}
	return held, settled, err
}

// deliverWake has the Waker type the wake a notice held. It types before it
// claims the row, inside the run's admission, so a terminal that cannot take
// the line leaves the notice queued for the next sweep and no second
// delivery types meanwhile. A wake nobody asked for is typed as shown since
// the notice appeared (domain.WithWakeShownSince) while a member is still
// connected in control.
func (s *Service) deliverWake(ctx context.Context, msg *store.RoomMessage, approver domain.MemberID, admit func(func() error) error) (Result, error) {
	s.mu.Lock()
	wake := s.waker
	s.mu.Unlock()
	wakeCtx := ctx
	if approver != "" {
		wakeCtx = domain.WithWakeApproved(ctx)
	}
	if admit == nil {
		admit = func(fn func() error) error {
			return s.cfg.Control.AdmitSnapshot(string(msg.RunID), func(current control.Snapshot, controlled bool) error {
				if controlled && current.Connected {
					wakeCtx = domain.WithWakeShownSince(ctx, msg.CreatedAt)
				}
				return fn()
			})
		}
	}
	var (
		receipt Receipt
		failure *store.RoomMessageFailure
	)
	err := admit(func() error {
		current, err := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
		if err != nil || current.State != store.RoomMessageQueued || wake == nil {
			return err
		}
		typed, err := wake(wakeCtx, msg.RunID)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrWakeNotTyped, err)
		}
		claimed, err := s.cfg.Store.ClaimRoomMessage(ctx, msg.ID, s.now(), approver != "", string(approver))
		if err != nil || !claimed {
			return err
		}
		receipt = ReceiptSent
		if !typed {
			receipt, failure = ReceiptNotSent, &store.RoomMessageFailure{Code: "nothing_to_wake", Message: "nothing was typed: the agent had read its mail, or its terminal no longer takes the line"}
		}
		return nil
	})
	if approver == "" && errors.Is(err, ErrWakeNotTyped) {
		// An overdue notice whose line the terminal cannot take yet is not
		// a failed delivery: the next sweep tries again.
		slog.Debug("collab: mail wake postponed", "run", msg.RunID, "message", msg.ID, "error", err)
		return Result{Message: msg}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if receipt == "" {
		stored, getErr := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
		if getErr != nil {
			return Result{}, getErr
		}
		return Result{Message: stored, Receipt: receiptForState(stored.State)}, nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentDeliveryTimeout)
	defer cancel()
	return s.settleResult(settleCtx, msg, receipt, "", failure)
}
