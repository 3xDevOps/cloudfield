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
// delivered by the Waker instead of as its own text. It reports whether a
// notice stands for the mail, which is false when nobody is connected in
// control. One key makes one notice. A busy run admission fails with
// control.ErrAdmissionBusy instead of being waited for.
func (s *Service) HoldWake(ctx context.Context, runID domain.RunID, key, notice string) (bool, error) {
	open, ok := s.cfg.Store.(store.OpenRoomRequestStore)
	if !ok || s.cfg.Control == nil {
		return false, nil
	}
	held := false
	var created *store.RoomMessage
	// The caller holds a coordination reference to the run, which cleanup
	// inside this admission waits for.
	err := s.cfg.Control.TryAdmitSnapshot(string(runID), func(current control.Snapshot, controlled bool) error {
		requests, err := open.ListOpenRoomRequests(ctx, runID)
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
		if err != nil || run.Protected || !controlled || !current.Connected {
			return err
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
	return held, err
}

// deliverWake has the Waker type the wake a notice held. It types before it
// claims the row, inside the run's admission, so a terminal that cannot take
// the line leaves the notice queued for the next sweep and no second
// delivery types meanwhile.
func (s *Service) deliverWake(ctx context.Context, msg *store.RoomMessage, approver domain.MemberID, admit func(func() error) error) (Result, error) {
	s.mu.Lock()
	wake := s.waker
	s.mu.Unlock()
	if admit == nil {
		admit = s.deliveryAdmission(msg.RunID)
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
		wakeCtx := ctx
		if approver != "" {
			wakeCtx = domain.WithWakeApproved(ctx)
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
