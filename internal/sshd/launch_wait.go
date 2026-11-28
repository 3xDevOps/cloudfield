package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func init() {
	registerMethod(protocol.MethodLaunchWaitingList, (*Server).launchWaitingList)
	registerMethod(protocol.MethodLaunchWaitingCancel, (*Server).launchWaitingCancel)
}

const (
	// maxWaitingLaunches is how many launches one member may have waiting
	// for room. One more is refused as it was before launches waited.
	maxWaitingLaunches = 8
	// launchWaitLimit is how long a launch waits for room.
	launchWaitLimit = 24 * time.Hour
	// launchRetryEvery is how often the oldest waiting launch is tried
	// with no sign of room, and launchRoomGap the least time between two
	// tries that a sign of room asks for.
	launchRetryEvery = 30 * time.Second
	launchRoomGap    = 5 * time.Second
	// waitingLaunchOp names top-level launches in the capacity counters.
	waitingLaunchOp = "launch"
)

// launchAttempt is on the context of the request that starts a waiting
// launch: the intent it starts, and the launch's own error when it fails.
type launchAttempt struct {
	intent  *domain.LaunchIntent
	refused error
}

type launchAttemptKey struct{}

func attemptOf(ctx context.Context) *launchAttempt {
	a, _ := ctx.Value(launchAttemptKey{}).(*launchAttempt)
	return a
}

func wireWaiting(in *domain.LaunchIntent) *protocol.LaunchWaiting {
	var p protocol.RunLaunchParams
	_ = json.Unmarshal(in.Params, &p)
	w := &protocol.LaunchWaiting{
		ID: string(in.ID), WorkspaceID: string(in.WorkspaceID), Harness: p.Harness, Task: p.Task, State: string(in.State), Refusal: in.Refusal,
		Attempts: in.Attempts, Passed: in.Passed, IdempotencyKey: in.IdempotencyKey,
		CreatedAt: in.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: in.CreatedAt.Add(launchWaitLimit).UTC().Format(time.RFC3339),
	}
	if in.EndedAt != nil {
		w.EndedAt = in.EndedAt.UTC().Format(time.RFC3339)
	}
	return w
}

// waitForRoom keeps the launch p, which refusal refused for want of room,
// to start when there is room. The base is captured now, so the wait does
// not move it. Over the member's cap the refusal stands.
func (s *Server) waitForRoom(ctx context.Context, member domain.MemberID, key string, p protocol.RunLaunchParams, refusal error) (*domain.LaunchIntent, *protocol.Error) {
	base, err := s.cfg.Runs.CaptureLaunchBase(ctx, domain.WorkspaceID(p.WorkspaceID), p.CachedBase)
	if err != nil {
		return nil, rpcError(err)
	}
	params, err := json.Marshal(p)
	if err != nil {
		return nil, rpcError(err)
	}
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	in, existing, err := s.cfg.Store.CreateLaunchIntent(ctx, &domain.LaunchIntent{
		MemberID: member, WorkspaceID: domain.WorkspaceID(p.WorkspaceID), IdempotencyKey: key, Method: protocol.MethodRunLaunch, Params: params,
		Base: base, Via: domain.ViaFrom(ctx), Connector: id.connector, Refusal: refusal.Error(),
	}, maxWaitingLaunches)
	if errors.Is(err, store.ErrTooManyWaiting) {
		return nil, rpcError(refusal)
	}
	if err != nil {
		return nil, rpcError(err)
	}
	if !existing {
		s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWait, Op: waitingLaunchOp, Run: in.ID, Text: in.Refusal})
	}
	return in, nil
}

// countLaunchWait logs e as the scheduler logs its capacity events and adds
// it to its counter.
func (s *Server) countLaunchWait(ctx context.Context, e domain.CapacityEvent) {
	e.At = time.Now().UTC()
	slog.Info("launch: capacity: "+e.Kind, "op", e.Op, "cause", e.Cause, "run", e.Run, "waited", e.Waited, "text", e.Text)
	if err := s.cfg.Store.CountCapacityEvent(context.WithoutCancel(ctx), e); err != nil {
		slog.Warn("launch: capacity: count "+e.Kind, "run", e.Run, "error", err)
	}
}

// endedLaunchError answers a request whose launch waited and ended without
// a session: the same key never launches again.
func endedLaunchError(in *domain.LaunchIntent) *protocol.Error {
	code := protocol.CodeUnavailable
	if in.State == domain.IntentCancelled {
		code = protocol.CodeConflict
	}
	return &protocol.Error{Code: code, Message: fmt.Sprintf("%s: the launch under idempotency_key %q waited for room and ended %s: %s; use a new key to launch again",
		protocol.MethodRunLaunch, in.IdempotencyKey, in.State, in.Refusal)}
}

func (s *Server) launchWaitingList(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.LaunchWaitingListParams](params)
	if perr != nil {
		return nil, perr
	}
	result := protocol.LaunchWaitingListResult{Waiting: []protocol.LaunchWaiting{}}
	if p.ID != "" {
		in, perr := s.ownLaunchIntent(ctx, member, p.ID, protocol.MethodLaunchWaitingList)
		if perr != nil {
			return nil, perr
		}
		result.Waiting = append(result.Waiting, *wireWaiting(in))
		return result, nil
	}
	list, err := s.cfg.Store.LaunchIntents(ctx, member, domain.IntentWaiting, domain.IntentStarting, domain.IntentCancelled, domain.IntentExpired, domain.IntentRefused)
	if err != nil {
		return nil, rpcError(err)
	}
	recent := time.Now().Add(-launchWaitLimit)
	for _, in := range list {
		if in.State.Open() || (in.State != domain.IntentCancelled && in.EndedAt != nil && in.EndedAt.After(recent)) {
			result.Waiting = append(result.Waiting, *wireWaiting(in))
		}
	}
	return result, nil
}

// ownLaunchIntent returns the waiting launch id of member, or of anyone for
// a super admin. Another member's is not found.
func (s *Server) ownLaunchIntent(ctx context.Context, member domain.MemberID, id, method string) (*domain.LaunchIntent, *protocol.Error) {
	in, err := s.cfg.Store.GetLaunchIntent(ctx, domain.RunID(id))
	if err == nil && in.MemberID != member && !s.superAdmin(ctx, member) {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: no waiting launch %s", method, id)}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	return in, nil
}

func (s *Server) launchWaitingCancel(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.LaunchWaitingCancelParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.ID == "" {
		return nil, invalidParams("id is required")
	}
	in, perr := s.ownLaunchIntent(ctx, member, p.ID, protocol.MethodLaunchWaitingCancel)
	if perr != nil {
		return nil, perr
	}
	now := time.Now()
	err := s.cfg.Store.EndLaunchIntent(ctx, in.ID, domain.IntentCancelled, "cancelled by "+string(member), now)
	if errors.Is(err, store.ErrConflict) {
		if in, perr = s.ownLaunchIntent(ctx, member, p.ID, protocol.MethodLaunchWaitingCancel); perr != nil {
			return nil, perr
		}
		if in.State == domain.IntentStarted {
			return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("%s: launch %s has started; stop session %s instead", protocol.MethodLaunchWaitingCancel, in.ID, in.ID)}
		}
		return *wireWaiting(in), nil
	}
	if err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: "cancelled", Run: in.ID, Waited: now.Sub(in.CreatedAt)})
	if in, perr = s.ownLaunchIntent(ctx, member, p.ID, protocol.MethodLaunchWaitingCancel); perr != nil {
		return nil, perr
	}
	return *wireWaiting(in), nil
}

// launchNotifier pushes a notice to a member's devices.
type launchNotifier interface {
	Notify(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID, urgent bool, flush <-chan struct{}, current func(context.Context) bool) error
}

// launchEnded counts and tells the member of a waiting launch that ended
// without a session, as a session that needs them is pushed. It stays in
// launch.waiting.list for a day.
func (s *Server) launchEnded(ctx context.Context, in *domain.LaunchIntent, to domain.LaunchIntentState, reason string, now time.Time) {
	s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: string(to), Run: in.ID, Waited: now.Sub(in.CreatedAt), Text: reason})
	notifier, ok := s.cfg.Services.Push.(launchNotifier)
	if !ok {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := notifier.Notify(ctx, in.MemberID, "A waiting launch did not start", reason, in.ID, true, nil, nil); err != nil && ctx.Err() == nil {
			slog.Warn("launch: tell the member a waiting launch ended", "launch", in.ID, "member", in.MemberID, "error", err)
		}
	}()
}

// RoomAppeared tells the server a session ended or fell asleep: the waiting
// launches are tried at the next launchRoomGap tick, before the clock would.
func (s *Server) RoomAppeared() {
	select {
	case s.launchRoom <- struct{}{}:
	default:
	}
}

// dispatchLaunchIntents starts waiting launches until ctx ends: the oldest
// every launchRetryEvery, and at once, launchRoomGap apart, after
// RoomAppeared, so room that appears is offered to them first.
func (s *Server) dispatchLaunchIntents(ctx context.Context) {
	if s.cfg.Recovered != nil {
		select {
		case <-s.cfg.Recovered:
		case <-ctx.Done():
			return
		}
	}
	if err := s.cfg.Store.RecoverLaunchIntents(ctx); err != nil {
		slog.Warn("launch: recover the launches a stopped server left starting", "error", err)
	}
	tick := time.NewTicker(launchRoomGap)
	defer tick.Stop()
	var tried time.Time
	signalled := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.launchRoom:
			signalled = true
			continue
		case now := <-tick.C:
			if !signalled && now.Sub(tried) < launchRetryEvery {
				continue
			}
			signalled, tried = false, now
			s.startWaitingLaunches(ctx, now)
		}
	}
}

// startWaitingLaunches tries the waiting launches, the oldest first, until
// one is refused for want of room: the ones behind it need as much. One
// that waited launchWaitLimit ends expired; one a check refuses ends
// refused and does not hold up the next.
func (s *Server) startWaitingLaunches(ctx context.Context, now time.Time) {
	waiting, err := s.cfg.Store.LaunchIntents(ctx, "", domain.IntentWaiting)
	if err != nil {
		slog.Warn("launch: list waiting launches", "error", err)
		return
	}
	for _, in := range waiting {
		if ctx.Err() != nil {
			return
		}
		if now.Sub(in.CreatedAt) >= launchWaitLimit {
			reason := "no room within 24 hours; the last refusal: " + in.Refusal
			if err := s.cfg.Store.EndLaunchIntent(ctx, in.ID, domain.IntentExpired, reason, now); err == nil {
				s.launchEnded(ctx, in, domain.IntentExpired, reason, now)
			} else if !errors.Is(err, store.ErrConflict) {
				slog.Warn("launch: expire a waiting launch", "launch", in.ID, "error", err)
			}
			continue
		}
		if !s.startWaitingLaunch(ctx, in, now) {
			return
		}
	}
}

// startWaitingLaunch replays in's request as its member, so every check a
// fresh launch makes is made again now. It reports false when the server
// still has no room.
func (s *Server) startWaitingLaunch(ctx context.Context, in *domain.LaunchIntent, now time.Time) bool {
	if err := s.cfg.Store.ClaimLaunchIntent(ctx, in.ID, now); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			slog.Warn("launch: claim a waiting launch", "launch", in.ID, "error", err)
		}
		return true
	}
	var p protocol.RunLaunchParams
	if err := json.Unmarshal(in.Params, &p); err != nil {
		s.endRefused(ctx, in, "the stored request cannot be read: "+err.Error(), now)
		return true
	}
	// An intent whose request had no key carries its own ID as its key.
	if in.IdempotencyKey != string(in.ID) {
		p.IdempotencyKey = in.IdempotencyKey
	}
	params, err := json.Marshal(p)
	if err != nil {
		s.endRefused(ctx, in, "the stored request cannot be written: "+err.Error(), now)
		return true
	}
	// A launch asked for through a connector is tried as that connector,
	// with the scopes it has now, and not at all once it was removed.
	local := s.Local(in.MemberID)
	if in.Connector != "" {
		var c *store.Connector
		connectors, ok := s.cfg.Store.(store.ConnectorStore)
		err := error(store.ErrNotFound)
		if ok {
			c, err = connectors.GetConnector(ctx, in.Connector)
		}
		switch {
		case errors.Is(err, store.ErrNotFound) || err == nil && c.Member != in.MemberID:
			s.endRefused(ctx, in, fmt.Sprintf("the connector that asked for it (%s) was removed", in.Via), now)
			return true
		case err != nil:
			slog.Warn("launch: read the connector of a waiting launch", "launch", in.ID, "error", err)
			if err := s.cfg.Store.ReleaseLaunchIntent(ctx, in.ID, in.Refusal); err != nil && !errors.Is(err, store.ErrConflict) {
				slog.Warn("launch: return a launch to waiting", "launch", in.ID, "error", err)
			}
			return true
		}
		local = s.ConnectorLocal(ConnectorGrant{Member: c.Member, Device: c.Device, Provider: c.Provider, Subject: c.Subject}, c.ID, c.ClientName, c.Scopes)
	}
	attempt := &launchAttempt{intent: in}
	_, perr := local.Call(domain.WithVia(context.WithValue(ctx, launchAttemptKey{}, attempt), in.Via), in.Method, params)
	switch {
	case perr == nil:
		slog.Info("launch: a waiting launch started", "launch", in.ID, "member", in.MemberID, "waited", now.Sub(in.CreatedAt), "attempts", in.Attempts+1, "passed", in.Passed)
		s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: "started", Run: in.ID, Waited: now.Sub(in.CreatedAt)})
		return true
	case ctx.Err() != nil:
		// A server stopping: the next one returns it to waiting.
		return false
	case scheduler.IsCapacityRefusal(attempt.refused):
		if err := s.cfg.Store.ReleaseLaunchIntent(ctx, in.ID, attempt.refused.Error()); err != nil && !errors.Is(err, store.ErrConflict) {
			slog.Warn("launch: return a launch to waiting", "launch", in.ID, "error", err)
		}
		return false
	}
	s.endRefused(ctx, in, perr.Message, now)
	return true
}

// endRefused ends a claimed launch refused, unless a cancel or its own
// session's creation settled it first.
func (s *Server) endRefused(ctx context.Context, in *domain.LaunchIntent, reason string, now time.Time) {
	err := s.cfg.Store.EndLaunchIntent(ctx, in.ID, domain.IntentRefused, reason, now)
	switch {
	case err == nil:
		s.launchEnded(ctx, in, domain.IntentRefused, reason, now)
	case errors.Is(err, store.ErrConflict):
		// Cancelled meanwhile, or its session was created and then failed,
		// which the session itself shows.
		if after, getErr := s.cfg.Store.GetLaunchIntent(ctx, in.ID); getErr == nil && after.State == domain.IntentStarted {
			s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: "started", Run: in.ID, Waited: now.Sub(in.CreatedAt)})
		}
	default:
		slog.Warn("launch: end a waiting launch refused", "launch", in.ID, "error", err)
	}
}
