package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

var ErrACPItemNotFound = errors.New("scheduler: no such conversation item")

var errNoItemLog = errors.New("scheduler: the session has no conversation item log")

type ACPStream struct {
	Replay []acphost.Item
	// Reset means the cursor is outside retained history: Replay contains
	// only real retained items and the viewer drops its previous projection.
	Reset           bool
	TruncatedBefore bool
	Epoch           int64
	Seq             int64
	// OldestSeq is the first replayed seq when the replay does not continue
	// from the viewer's cursor; the viewer pages older items from it.
	OldestSeq int64
	// Items carries every later item while the session is live. It is nil
	// when no session is live, and closed when the session ends or the
	// viewer falls too far behind; either way the viewer resubscribes.
	Items <-chan acphost.Item
	// Started is closed when the viewer should resubscribe, for a stream
	// that opened with no live session: the next session started, or the
	// closing session it found has ended.
	Started <-chan struct{}
	State   *acphost.State
	Cancel  func()
}

func (s *Scheduler) ACPSubscribe(run domain.RunID, afterSeq int64) (ACPStream, error) {
	sess, started, release := s.acp.started(run)
	if sess != nil {
		replay, items, cancel, err := sess.Subscribe(afterSeq)
		if err == nil {
			state := sess.State()
			out := ACPStream{Replay: replay.Items, Reset: replay.Reset, TruncatedBefore: replay.TruncatedBefore, Items: items, State: &state, Cancel: cancel, Seq: replay.Seq, Epoch: replay.Epoch, OldestSeq: replay.OldestSeq}
			return out, nil
		}
		if !errors.Is(err, acphost.ErrClosed) {
			return ACPStream{}, err
		}
		// The session is closing: the viewer resubscribes once it is gone.
		started = sess.Done()
	}
	out := ACPStream{Started: started, Cancel: release}
	log, err := s.openItemLog(run)
	if err != nil {
		release()
		return ACPStream{}, err
	}
	if log == nil {
		return out, nil
	}
	defer func() { _ = log.Close() }()
	replay, err := log.Replay(afterSeq)
	if err != nil {
		release()
		return ACPStream{}, err
	}
	out.Replay, out.Reset, out.TruncatedBefore = replay.Items, replay.Reset, replay.TruncatedBefore
	out.Epoch, out.Seq, out.OldestSeq = replay.Epoch, replay.Seq, replay.OldestSeq
	return out, nil
}

func (s *Scheduler) ACPPending(run domain.RunID) []acphost.Request {
	sess := s.acp.session(run)
	if sess == nil {
		return nil
	}
	return sess.State().Pending
}

func (s *Scheduler) ACPHistory(run domain.RunID, beforeSeq int64, limit int) (acphost.HistoryPage, error) {
	if beforeSeq <= 0 {
		beforeSeq = math.MaxInt64
	}
	var page acphost.HistoryPage
	err := s.withItemLog(run, func(log *acphost.Log) (err error) {
		page, err = log.History(beforeSeq, limit)
		return err
	})
	if errors.Is(err, errNoItemLog) {
		return acphost.HistoryPage{}, nil
	}
	return page, err
}

func (s *Scheduler) ACPItem(run domain.RunID, seq int64) (acphost.Item, error) {
	var item acphost.Item
	err := s.withItemLog(run, func(log *acphost.Log) error {
		var found bool
		var err error
		item, found, err = log.Item(seq)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %d", ErrACPItemNotFound, seq)
		}
		return nil
	})
	if errors.Is(err, errNoItemLog) {
		err = fmt.Errorf("%w: %d", ErrACPItemNotFound, seq)
	}
	return item, err
}

func (s *Scheduler) withItemLog(run domain.RunID, read func(*acphost.Log) error) error {
	if sess := s.acp.session(run); sess != nil {
		if err := read(sess.Log()); !errors.Is(err, acphost.ErrLogClosed) {
			return err
		}
	}
	log, err := s.openItemLog(run)
	if err != nil {
		return err
	}
	if log == nil {
		return errNoItemLog
	}
	defer func() { _ = log.Close() }()
	return read(log)
}

func (s *Scheduler) recordedMode(run domain.RunID) string {
	log, err := s.openItemLog(run)
	if err != nil || log == nil {
		return ""
	}
	defer func() { _ = log.Close() }()
	return log.Mode()
}

func (s *Scheduler) openItemLog(run domain.RunID) (*acphost.Log, error) {
	log, err := s.itemLogs.Open(s.cfg.PTY.ItemLogPath(run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return log, err
}

func (s *Scheduler) ACPAnswer(run domain.RunID, requestID, optionID string, values map[string]any) error {
	awake, err := s.holdAwakeNow(run)
	if err != nil {
		return err
	}
	defer awake()
	sess, err := s.acp.live(context.Background(), run)
	if err != nil {
		return err
	}
	return sess.Answer(requestID, optionID, values)
}

func (s *Scheduler) ACPCancel(ctx context.Context, run domain.RunID) error {
	awake, err := s.holdAwakeNow(run)
	if errors.Is(err, errAsleep) {
		return nil
	}
	if err != nil {
		return err
	}
	defer awake()
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	return sess.Cancel(ctx)
}

// ErrNoInterrupt refuses an agent that stops a turn only on a key pressed
// in its own terminal: typing a guessed key there could quit it instead.
var ErrNoInterrupt = errors.New("scheduler: the session's agent takes an interrupt only in its own terminal")

// Interrupt asks run's agent to stop the turn it is in: an Enhanced
// session's over ACP, a Standard Codex terminal's through its app-server.
// stopped waits for that app-server's answer, for the caller to take
// outside control admission. A session that fell asleep idle has no turn to
// stop.
func (s *Scheduler) Interrupt(ctx context.Context, run *domain.Run, actor domain.MemberID) (stopped func() error, err error) {
	defer func() {
		if err != nil {
			return
		}
		// The interrupt is accepted, so recording who sent it must not
		// depend on the caller staying connected.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerRecordTimeout)
		defer cancel()
		s.publishTimeline(recordCtx, run.WorkspaceID, run.ID, actor, events.TimelineNote, "sent an interrupt")
	}()
	s.mu.Lock()
	var appServer, sleeping, asleep, midTurn, paused bool
	var cid runtime.ID
	if entry := s.runs[run.ID]; entry != nil {
		appServer, sleeping, paused, cid = entry.appServer, entry.sleeping, entry.paused, entry.containerID
		asleep = !entry.asleepSince.IsZero()
		// A failed wake drops the memory guard's mark and saves a working
		// VM again with its turn.
		midTurn = asleep && (entry.memorySaved > 0 || entry.activity == domain.ActivityWorking)
	}
	s.mu.Unlock()
	stopped = func() error { return nil }
	switch {
	case !run.ACP && run.Status != domain.RunRunning && run.Status != domain.RunNeedsAttention:
		return nil, fmt.Errorf("%w: the session is %s", ErrNoLiveEnvironment, run.Status)
	case !run.ACP && !appServer:
		return nil, fmt.Errorf("%w: run cloudfield attach %s and press the agent's own interrupt key", ErrNoInterrupt, run.ID)
	case midTurn:
		return nil, fmt.Errorf("%w: the session was saved to disk under memory pressure, and any turn it was in carries on when it wakes: run cloudfield resume %s first", ErrInvalidTransition, run.ID)
	case run.ACP:
		return stopped, s.ACPCancel(ctx, run.ID)
	case sleeping:
		return nil, errFallingAsleep
	case asleep:
		return stopped, nil
	case paused:
		return nil, fmt.Errorf("%w: the session is paused: run cloudfield resume %s first", ErrInvalidTransition, run.ID)
	case s.vmFrozen(cid):
		// A frozen app-server answers nothing, and the caller holds control
		// admission until this returns.
		return nil, fmt.Errorf("%w: the server froze the session's VM while it is short of memory, and it carries on by itself once memory is free again: interrupt it then", ErrInvalidTransition)
	}
	client := s.appServer.connected(run.ID)
	if client == nil {
		return nil, ErrAppServerUnavailable
	}
	return client.Interrupt(ctx)
}

func (s *Scheduler) ACPSetOption(ctx context.Context, run domain.RunID, optionID string, value any) error {
	awake, err := s.holdAwakeNow(run)
	if err != nil {
		return err
	}
	defer awake()
	if err = s.optionsAnswered(run); err != nil {
		return err
	}
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	isModel := optionID == sess.ModelOptionID()
	effortID := sess.EffortOptionID()
	if err = sess.SetOption(ctx, optionID, value); err != nil {
		return err
	}
	if effortID != "" && optionID == effortID {
		effort := sess.Effort()
		// Claude Code's adapter offers "default", which its CLI flag refuses.
		if effort == "default" {
			effort = ""
		}
		var stored *domain.Run
		if stored, err = s.cfg.Store.GetRun(ctx, run); err != nil {
			return fmt.Errorf("scheduler: load session to record its effort: %w", err)
		}
		if effort == stored.Effort {
			return nil
		}
		if err = domain.ValidEffort(effort); err != nil {
			if nerr := sess.Notice("error", "Effort not recorded", err.Error()); nerr != nil {
				slog.Warn("scheduler: record the effort notice", "run", run, "error", nerr)
			}
			return nil
		}
		if err = s.cfg.Store.SetRunEffort(ctx, run, effort); err != nil {
			return fmt.Errorf("scheduler: record the session's effort: %w", err)
		}
		s.publish(ctx, events.Event{WorkspaceID: stored.WorkspaceID, RunID: run, Payload: events.RunEffortPayload{Effort: effort}})
		return nil
	}
	model := sess.Model()
	if !isModel || model == "" {
		return nil
	}
	stored, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("scheduler: load session to record its model: %w", err)
	}
	if model == stored.Model && stored.ModelError == "" {
		return nil
	}
	if err = domain.ValidModel(model); err != nil {
		if nerr := sess.Notice("error", "Model not recorded", err.Error()); nerr != nil {
			slog.Warn("scheduler: record the model notice", "run", run, "error", nerr)
		}
		return nil
	}
	if err = s.cfg.Store.SetRunModel(ctx, run, model); err != nil {
		return fmt.Errorf("scheduler: record the session's model: %w", err)
	}
	s.publish(ctx, events.Event{WorkspaceID: stored.WorkspaceID, RunID: run, Payload: events.RunModelPayload{Model: model}})
	return nil
}

// optionsAnswered refuses a session that is paused or whose VM is frozen:
// its agent answers nothing, and the caller holds control admission until
// its call returns. It goes before the live session, which a session paused
// across a server restart lacks until its resume.
func (s *Scheduler) optionsAnswered(run domain.RunID) error {
	s.mu.Lock()
	var paused bool
	var cid runtime.ID
	if entry := s.runs[run]; entry != nil {
		paused, cid = entry.paused, entry.containerID
	}
	s.mu.Unlock()
	switch {
	case paused:
		return fmt.Errorf("%w: the session is paused: run cloudfield resume %s first", ErrInvalidTransition, run)
	case s.vmFrozen(cid):
		return fmt.Errorf("%w: the server froze the session's VM while it is short of memory, and it carries on by itself once memory is free again: change it then", ErrInvalidTransition)
	}
	return nil
}

// ErrNoSuchOption refuses a model or an effort for a session whose agent
// offers no option that sets it.
var ErrNoSuchOption = errors.New("scheduler: the session's agent offers no such option")

// ACPSetModelEffort is ACPSetOption for a caller that names a model, an
// effort or both and has no option ids; an empty one is left as it is. It
// returns the two as the agent reports them afterwards, which can differ
// from the values asked for, and records what it set on the timeline with
// actor.
func (s *Scheduler) ACPSetModelEffort(ctx context.Context, run *domain.Run, actor domain.MemberID, model, effort string) (nowModel, nowEffort string, err error) {
	awake, err := s.holdAwakeNow(run.ID)
	if err != nil {
		return "", "", err
	}
	defer awake()
	if err = s.optionsAnswered(run.ID); err != nil {
		return "", "", err
	}
	sess, err := s.acp.live(ctx, run.ID)
	if err != nil {
		return "", "", err
	}
	var set []string
	defer func() {
		if len(set) == 0 {
			return
		}
		if err != nil {
			err = fmt.Errorf("%w; the session is now set to %s", err, strings.Join(set, ", "))
		}
		// The agent already has the change, so recording who made it must
		// not depend on the caller staying connected.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerRecordTimeout)
		defer cancel()
		s.publishTimeline(recordCtx, run.WorkspaceID, run.ID, actor, events.TimelineNote, "set "+strings.Join(set, ", "))
	}()
	// The model goes first: the levels an agent offers can depend on it.
	for _, option := range []struct {
		name, value string
		id, now     func() string
	}{{"model", model, sess.ModelOptionID, sess.Model}, {"effort", effort, sess.EffortOptionID, sess.Effort}} {
		if option.value == "" {
			continue
		}
		id := option.id()
		if id == "" {
			return "", "", fmt.Errorf("%w: %s", ErrNoSuchOption, option.name)
		}
		if err = s.ACPSetOption(ctx, run.ID, id, option.value); err != nil {
			return "", "", fmt.Errorf("set the session's %s: %w", option.name, err)
		}
		set = append(set, option.name+" "+option.now())
	}
	return sess.Model(), sess.Effort(), nil
}

var errACPBusy = errors.New("scheduler: the Enhanced agent is busy")

// Wakeable reports whether the server can prompt run's agent session: an
// idle enhanced session, a Standard Codex terminal attached to its
// app-server, or a sleeping Standard session VM.
func (s *Scheduler) Wakeable(run domain.RunID) bool {
	if s.asleepStandardVM(run) {
		return true
	}
	if s.attachedAppServer(run) {
		return s.appServer.connected(run) != nil
	}
	return s.IdleEnhanced(run)
}

// Wake prompts run's agent with prompt, text coord generates, naming the
// command its container has. The turn-end report reminder keeps the old
// name: released dashboards recognise it by its lead.
func (s *Scheduler) Wake(ctx context.Context, run domain.RunID, prompt string) error {
	if !strings.HasPrefix(prompt, coordhooks.ReportReminderLead) {
		s.mu.Lock()
		var cid runtime.ID
		if entry := s.runs[run]; entry != nil {
			cid = entry.containerID
		}
		s.mu.Unlock()
		prompt = coordhooks.Named(prompt, s.containerCLI(ctx, cid))
	}
	if s.asleepStandardVM(run) {
		// The save cut the agent's mail hook and app-server bridge; both
		// connect again once the VM runs. The hook then takes the mail.
		if err := s.WakeRun(ctx, run, ""); err != nil {
			return err
		}
		if !s.attachedAppServer(run) {
			return nil
		}
	}
	if !s.attachedAppServer(run) {
		return s.WakeEnhanced(ctx, run, prompt)
	}
	return s.appServer.wake(ctx, run, prompt)
}

// asleepStandardVM reports an open Standard session whose VM is saved or
// being saved.
func (s *Scheduler) asleepStandardVM(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.launchMode != domain.LaunchTUI || entry.retained || (!entry.sleeping && entry.asleepSince.IsZero()) {
		return false
	}
	_, saving := s.saver(entry.containerID)
	return saving
}

// InTurn reports a Standard Codex terminal in a turn; an enhanced session
// is woken only when idle.
func (s *Scheduler) InTurn(run domain.RunID) bool {
	if !s.attachedAppServer(run) {
		return false
	}
	client := s.appServer.connected(run)
	return client != nil && client.Busy()
}

// attachedAppServer reports a live, unpaused run whose terminal is attached
// to its app-server.
func (s *Scheduler) attachedAppServer(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	return entry != nil && entry.appServer && !entry.paused
}

// IdleEnhanced counts a sleeping open Enhanced session as idle: a prompt
// wakes it.
func (s *Scheduler) IdleEnhanced(run domain.RunID) bool {
	s.mu.Lock()
	entry := s.runs[run]
	asleep := entry != nil && entry.launchMode == domain.LaunchACP && !entry.retained && (entry.sleeping || !entry.asleepSince.IsZero())
	enhanced := entry != nil && entry.launchMode == domain.LaunchACP && !entry.paused && entry.switching == ""
	s.mu.Unlock()
	if asleep {
		return true
	}
	if !enhanced {
		return false
	}
	sess := s.acp.session(run)
	if sess == nil {
		return false
	}
	st := sess.State()
	return !st.TurnInFlight && st.Queued == 0
}

func (s *Scheduler) WakeEnhanced(ctx context.Context, run domain.RunID, prompt string) error {
	if !s.IdleEnhanced(run) {
		return errACPBusy
	}
	// A wake this starts outlasts the delivery's deadline; mail the deadline
	// cut off is delivered on the coordination service's retry to the woken
	// session.
	awake, err := s.holdAwake(ctx, run)
	if err != nil {
		return err
	}
	defer awake()
	if !s.IdleEnhanced(run) {
		return errACPBusy
	}
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	blocks := []acp.ContentBlock{acp.TextBlock(prompt)}
	instructions, settle := s.acp.claimInstructions(run, sess)
	if instructions != "" {
		blocks = append(blocks, acp.TextBlock("\n\n"+instructions))
	}
	started, err := sess.PromptIdle(ctx, blocks, settle)
	if err == nil && !started {
		err = errACPBusy
	}
	if !started && ctx.Err() == nil {
		// No turn took the prompt; one awaiting acceptance settles through
		// the callback.
		settle(err)
	}
	return err
}
