package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// launchReclaimEvery is how often a try may free disk as a launch
	// somebody asks for does. Reclaim removes what closed and sleeping
	// sessions keep, so the other tries leave it alone.
	launchReclaimEvery = 10 * time.Minute
	// launchIntentsKept is how long an ended waiting launch is kept, to
	// answer its idempotency key.
	launchIntentsKept = 7 * 24 * time.Hour
	// maxWaitingParams bounds the request a waiting launch stores; a
	// larger one is refused as a launch always was.
	maxWaitingParams = 64 << 10
	// maxDailyWaiting bounds the waiting launches one member may make in
	// a day, cancelled and ended ones included: they are kept a while.
	maxDailyWaiting = 200
	// maxLaunchFailures is how many tries in a row may fail on something
	// that is neither room nor a refusal before the launch ends refused.
	maxLaunchFailures = 5
	// waitingTaskBytes is how much of the task stays on an ended launch.
	waitingTaskBytes = 200
	// waitingLaunchOp names top-level launches in the capacity counters.
	waitingLaunchOp = "launch"
)

// launchAttempt is on the context of the request that starts a waiting
// launch: the intent it starts, and the launch's own error when it fails.
type launchAttempt struct {
	intent *domain.LaunchIntent
	// reclaim, when set, lets this try free disk when disk is what it
	// lacks, and spends the allowance when it does.
	reclaim func()
	// created closes once the session's row exists, and refused is the
	// launch's own error when it fails.
	created chan struct{}
	refused error
}

type launchAttemptKey struct{}

func attemptOf(ctx context.Context) *launchAttempt {
	a, _ := ctx.Value(launchAttemptKey{}).(*launchAttempt)
	return a
}

func wireWaiting(in *domain.LaunchIntent) *protocol.LaunchWaiting {
	w := &protocol.LaunchWaiting{
		ID: string(in.ID), WorkspaceID: string(in.WorkspaceID), Harness: in.Harness, Task: in.Task, State: string(in.State), Refusal: in.Refusal,
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
	// What is refused here was not kept, so its refusal must not say it
	// waits: a launch sent behind the waiting ones is refused for them.
	notKept := func(why string) *protocol.Error {
		reason := refusal.Error()
		if errors.Is(refusal, scheduler.ErrLaunchesWaiting) {
			reason = "launches asked for earlier are waiting for room on the server"
		}
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf("%s: %s; this launch was not kept to wait: %s", protocol.MethodRunLaunch, reason, why)}
	}
	params, err := json.Marshal(p)
	if err != nil {
		return nil, rpcError(err)
	}
	if len(params) > maxWaitingParams {
		return nil, notKept(fmt.Sprintf("its request is %d bytes and a waiting launch stores at most %d", len(params), maxWaitingParams))
	}
	base, err := s.cfg.Runs.CaptureLaunchBase(ctx, domain.WorkspaceID(p.WorkspaceID), p.CachedBase)
	if err != nil {
		// The launch could not start with room either: its own error,
		// with its code, is the answer.
		return nil, rpcError(err)
	}
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	in, existing, err := s.cfg.Store.CreateLaunchIntent(ctx, &domain.LaunchIntent{
		MemberID: member, WorkspaceID: domain.WorkspaceID(p.WorkspaceID), IdempotencyKey: key, Method: protocol.MethodRunLaunch, Params: params,
		Harness: p.Harness, Task: strings.ToValidUTF8(p.Task[:min(len(p.Task), waitingTaskBytes)], ""),
		Base: base, Via: domain.ViaFrom(ctx), Connector: id.connector, Device: id.device, Refusal: refusal.Error(),
	}, maxWaitingLaunches, maxDailyWaiting)
	if errors.Is(err, store.ErrTooManyWaiting) {
		return nil, notKept(fmt.Sprintf("one member may have %d launches waiting, and make %d in a day", maxWaitingLaunches, maxDailyWaiting))
	}
	if err != nil {
		return nil, rpcError(err)
	}
	if !existing {
		s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWait, Op: waitingLaunchOp, Run: in.ID, Text: in.Refusal})
	}
	// One sent behind the others was refused no room: it is tried soon.
	if errors.Is(refusal, scheduler.ErrLaunchesWaiting) {
		s.RoomAppeared()
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
	list, err := s.cfg.Store.LaunchIntents(ctx, member, time.Now().Add(-launchWaitLimit))
	if err != nil {
		return nil, rpcError(err)
	}
	for _, in := range list {
		// What its own member cancelled needs no telling.
		if in.State.Open() || in.Refusal != cancelledBy(in.MemberID) {
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
	err := s.cfg.Store.EndLaunchIntent(ctx, in.ID, domain.IntentCancelled, cancelledBy(member), now)
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
	if member == in.MemberID {
		s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: "cancelled", Run: in.ID, Waited: now.Sub(in.CreatedAt)})
	} else {
		s.launchEnded(s.authCtx(), in, domain.IntentCancelled, cancelledBy(member), now)
	}
	if in, perr = s.ownLaunchIntent(ctx, member, p.ID, protocol.MethodLaunchWaitingCancel); perr != nil {
		return nil, perr
	}
	return *wireWaiting(in), nil
}

func cancelledBy(member domain.MemberID) string { return "cancelled by " + string(member) }

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
	if !ok || !s.beginHandler() {
		return
	}
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
	s.launchPass.Lock()
	defer s.launchPass.Unlock()
	if last := time.Unix(0, s.launchPruned.Load()); now.Sub(last) >= time.Hour {
		s.launchPruned.Store(now.UnixNano())
		if err := s.cfg.Store.PruneLaunchIntents(ctx, now.Add(-launchIntentsKept)); err != nil {
			slog.Warn("launch: delete old waiting launches", "error", err)
		}
	}
	// Passes do not overlap and a try holds its claim only within one, so
	// an intent still starting was left by a try whose last write failed.
	if err := s.cfg.Store.RecoverLaunchIntents(ctx); err != nil {
		slog.Warn("launch: settle the launches a try left starting", "error", err)
	}
	waiting, err := s.cfg.Store.LaunchIntents(ctx, "", time.Time{})
	if err != nil {
		slog.Warn("launch: list waiting launches", "error", err)
		return
	}
	for _, in := range waiting {
		if ctx.Err() != nil {
			return
		}
		if in.State != domain.IntentWaiting {
			continue
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
	p.IdempotencyKey = in.IdempotencyKey
	params, err := json.Marshal(p)
	if err != nil {
		s.endRefused(ctx, in, "the stored request cannot be written: "+err.Error(), now)
		return true
	}
	// What admitted the request must still admit it: a launch asked for
	// through a connector is tried as that connector, with the grant and
	// scopes it has now, and one asked for from an edge device only while
	// that device is admitted.
	local := s.Local(in.MemberID)
	switch {
	case in.Connector != "":
		connectors, ok := s.cfg.Store.(store.ConnectorStore)
		if !ok {
			s.endRefused(ctx, in, fmt.Sprintf("the connector that asked for it (%s) cannot be checked on this server", in.Via), now)
			return true
		}
		c, err := connectors.GetConnector(ctx, in.Connector)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			// The read failed; whether the connector stands is not known.
			if in.Failures+1 < maxLaunchFailures {
				s.release(ctx, in, "the connector that asked for it could not be read: "+err.Error(), true)
			} else {
				s.endRefused(ctx, in, fmt.Sprintf("the connector that asked for it (%s) could not be read: %v", in.Via, err), now)
			}
			return true
		}
		if err == nil && (c.Member != in.MemberID || !c.RefreshExpiresAt.After(now)) {
			err = store.ErrNotFound
		}
		var grant ConnectorGrant
		if err == nil {
			grant = ConnectorGrant{Member: c.Member, Device: c.Device, Provider: c.Provider, Subject: c.Subject}
			err = s.CheckConnectorGrant(ctx, grant)
		}
		if err != nil {
			s.endRefused(ctx, in, fmt.Sprintf("the connector that asked for it (%s) is no longer admitted: %v", in.Via, err), now)
			return true
		}
		local = s.ConnectorLocal(grant, c.ID, c.ClientName, c.Scopes)
	case in.Device != "":
		if err := s.checkConnIdentity(ctx, connIdentity{member: in.MemberID, device: in.Device}); err != nil {
			s.endRefused(ctx, in, fmt.Sprintf("the device that asked for it is no longer admitted: %v", err), now)
			return true
		}
	}
	attempt := &launchAttempt{intent: in, created: make(chan struct{})}
	if now.Sub(time.Unix(0, s.launchReclaimed.Load())) >= launchReclaimEvery {
		attempt.reclaim = func() { s.launchReclaimed.Store(now.UnixNano()) }
	}
	if !s.beginHandler() {
		return false
	}
	done := make(chan *protocol.Error, 1)
	go func() {
		defer s.wg.Done()
		_, perr := local.Call(domain.WithVia(context.WithValue(ctx, launchAttemptKey{}, attempt), in.Via), in.Method, params)
		done <- perr
	}()
	started := func() bool {
		slog.Info("launch: a waiting launch started", "launch", in.ID, "member", in.MemberID, "waited", now.Sub(in.CreatedAt), "attempts", in.Attempts+1, "passed", in.Passed)
		s.countLaunchWait(ctx, domain.CapacityEvent{Kind: domain.CapacityWaitEnd, Op: waitingLaunchOp, Cause: "started", Run: in.ID, Waited: now.Sub(in.CreatedAt)})
		return true
	}
	var perr *protocol.Error
	select {
	case <-attempt.created:
		// The session exists and goes on provisioning by itself: what
		// fails from here is the session's to show. The next launch need
		// not wait for it.
		return started()
	case perr = <-done:
	}
	// Both can be ready: a session that exists is a start whatever its
	// provisioning then returned.
	select {
	case <-attempt.created:
		return started()
	default:
	}
	switch {
	case perr == nil:
		if _, err := s.cfg.Store.GetRun(ctx, in.ID); err == nil {
			// Its session existed already; the next pass settles the row.
			return started()
		}
		// Answered without this launch: its key already named a session.
		s.endRefused(ctx, in, "its idempotency key already answers with another session, so this one was not started", now)
		return true
	case ctx.Err() != nil:
		// A server stopping: the next one returns it to waiting.
		return false
	case scheduler.IsCapacityRefusal(attempt.refused):
		s.release(ctx, in, attempt.refused.Error(), false)
		return false
	case (perr.Code == protocol.CodeInternal || perr.Code == protocol.CodeUnavailable) && in.Failures+1 < maxLaunchFailures:
		// Not a refusal: a read or write that failed, or something that
		// could not be reached. It is tried again, a few times.
		s.release(ctx, in, perr.Message, true)
		return true
	}
	s.endRefused(ctx, in, perr.Message, now)
	return true
}

// release returns a claimed launch to waiting with the reason its try did
// not start it.
func (s *Server) release(ctx context.Context, in *domain.LaunchIntent, reason string, failed bool) {
	if err := s.cfg.Store.ReleaseLaunchIntent(ctx, in.ID, reason, failed); err != nil && !errors.Is(err, store.ErrConflict) {
		slog.Warn("launch: return a launch to waiting", "launch", in.ID, "error", err)
	}
}

// endRefused ends a claimed launch refused, unless a cancel or its own
// session's creation settled it first.
func (s *Server) endRefused(ctx context.Context, in *domain.LaunchIntent, reason string, now time.Time) {
	err := s.cfg.Store.EndLaunchIntent(ctx, in.ID, domain.IntentRefused, reason, now)
	switch {
	case err == nil:
		s.launchEnded(ctx, in, domain.IntentRefused, reason, now)
	case errors.Is(err, store.ErrConflict):
		// A cancel settled it first.
	default:
		slog.Warn("launch: end a waiting launch refused", "launch", in.ID, "error", err)
	}
}
