package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/collab"
	"github.com/3xDevOps/cloudfield/internal/control"
	"github.com/3xDevOps/cloudfield/internal/coord"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// noticeWaker stands in for the scheduler: refusal is its typed wake's
// guard saying the terminal cannot take the line now.
type noticeWaker struct {
	mu       sync.Mutex
	lines    []string
	approved int
	refusal  error
	enhanced atomic.Bool
	printing atomic.Bool
	// shown is whether each wake's line was shown to a member in control
	// who did not ask for it.
	shown  []bool
	onType func()
}

func (*noticeWaker) Wakeable(domain.RunID) bool    { return true }
func (*noticeWaker) InTurn(domain.RunID) bool      { return false }
func (w *noticeWaker) TypedWake(domain.RunID) bool { return !w.enhanced.Load() }
func (w *noticeWaker) Printing(domain.RunID) bool  { return w.printing.Load() }

func (w *noticeWaker) Wake(ctx context.Context, _ domain.RunID, prompt string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shown = append(w.shown, domain.WakeShown(ctx))
	if w.refusal != nil {
		return w.refusal
	}
	w.lines = append(w.lines, prompt)
	if domain.WakeApproved(ctx) {
		w.approved++
	}
	if w.onType != nil {
		w.onType()
	}
	return nil
}

func (w *noticeWaker) typed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

func (w *noticeWaker) lastShown() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shown[len(w.shown)-1]
}

func (w *noticeWaker) refuse(err error) {
	w.mu.Lock()
	w.refusal = err
	w.mu.Unlock()
}

type noticeFixture struct {
	*wakeServerFixture
	waker  *noticeWaker
	rooms  *collab.Service
	now    atomic.Pointer[time.Time]
	steers atomic.Int32
}

func newNoticeFixture(t *testing.T) *noticeFixture {
	t.Helper()
	return newNoticeFixtureFor(t, false)
}

func newNoticeFixtureFor(t *testing.T, subsession bool) *noticeFixture {
	t.Helper()
	f := &noticeFixture{wakeServerFixture: newWakeServerFixture(t, subsession), waker: &noticeWaker{}}
	start := time.Now().UTC()
	f.now.Store(&start)
	f.boot(t, nil)
	return f
}

// boot wires the room and coordination services as svc_coord.go does.
func (f *noticeFixture) boot(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	rooms, err := collab.New(collab.Config{
		Store: f.db, Runs: f.db, Workspaces: f.db, Bus: f.bus, Control: f.control, Ready: ready, WorkerInterval: 10 * time.Millisecond,
		Now: func() time.Time { return *f.now.Load() },
		Inject: func(context.Context, domain.RunID, domain.MemberID, domain.AgentPrompt, bool, func(error)) (string, error) {
			f.steers.Add(1)
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rooms.Close() })
	deps := Deps{Store: f.db, Control: f.control}
	svc, err := coord.New(coord.Config{Dir: filepath.Join(f.dir, "coord"), Store: f.db, Mail: f.db, Bus: f.bus, Peers: wakeNoPeers{},
		Waker: f.waker, WakeAdmission: newCoordWakeAdmission(deps), ReminderAdmission: newCoordReminderAdmission(deps), HoldWake: rooms.HoldWake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	rooms.SetWaker(svc.TypeHeldWake)
	if _, err = svc.Provision(context.Background(), f.run.ID, nil); err != nil {
		t.Fatal(err)
	}
	f.rooms, f.coord = rooms, svc
}

func (f *noticeFixture) takeControl(t *testing.T) control.Snapshot {
	t.Helper()
	lease, _, err := f.control.Acquire(string(f.run.ID), string(f.member.ID), "tab", false)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func (f *noticeFixture) mail(t *testing.T, key string) {
	t.Helper()
	msg := &store.RunMessage{WorkspaceID: f.run.WorkspaceID, FromRun: f.run.ID, ToRun: f.run.ID, Body: key, IdempotencyKey: key}
	if err := f.db.AppendRunMessage(context.Background(), msg, protocol.CoordMaxUnread); err != nil {
		t.Fatal(err)
	}
}

func (f *noticeFixture) readMail(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, token, _, err := f.db.DeliverRunMessages(ctx, f.run.ID, "", protocol.CoordMaxUnread)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.db.AckRunMessages(ctx, f.run.ID, token); err != nil {
		t.Fatal(err)
	}
}

func (f *noticeFixture) room(t *testing.T) []*store.RoomMessage {
	t.Helper()
	page, err := f.db.ListRoomMessages(context.Background(), f.run.WorkspaceID, f.run.ID, "", store.MaxCollaborationPageSize)
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// notice wakes the run's coordination and returns its room once it holds
// rows messages.
func (f *noticeFixture) notice(t *testing.T, rows int) []*store.RoomMessage {
	t.Helper()
	f.coord.WakeIdle(f.run.ID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		room := f.room(t)
		if len(room) == rows {
			return room
		}
		if time.Now().After(deadline) {
			t.Fatalf("the room holds %d messages, want %d", len(room), rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// woken wakes the run's coordination and waits for it to type a line.
func (f *noticeFixture) woken() {
	f.coord.WakeIdle(f.run.ID)
	deadline := time.Now().Add(3 * time.Second)
	for len(f.waker.typed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// settled gives a coordination wake pass time to do what it should not.
func (f *noticeFixture) settled() {
	f.coord.WakeIdle(f.run.ID)
	time.Sleep(300 * time.Millisecond)
}

func (f *noticeFixture) expire(t *testing.T) {
	t.Helper()
	later := f.now.Load().Add(collab.SteeringDelay + time.Second)
	f.now.Store(&later)
	if _, err := f.rooms.DeliverDue(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

func (f *noticeFixture) wantTyped(t *testing.T, unread ...int) {
	t.Helper()
	lines := f.waker.typed()
	if len(lines) != len(unread) {
		t.Fatalf("typed %q, want %d lines", lines, len(unread))
	}
	for i, n := range unread {
		if lines[i] != protocol.CoordInboxContext(n) {
			t.Fatalf("typed %q, want the inbox line alone: %q", lines[i], protocol.CoordInboxContext(n))
		}
	}
}

// The coordination builder takes the room service the collaboration builder
// attached, and builders run in the order their files' inits registered them.
func TestTheCollaborationServiceRegistersBeforeCoordination(t *testing.T) {
	index := func(name string) int {
		return slices.IndexFunc(serviceBuilders, func(b serviceBuilder) bool { return b.name == name })
	}
	if rooms, coordination := index("collaboration"), index("coord"); rooms < 0 || rooms > coordination {
		t.Fatalf("collaboration registers at %d and coord at %d, so coord is built without the room service that shows held wakes", rooms, coordination)
	}
}

func TestATypedWakeWithNobodyConnectedInControlIsTypedAtOnce(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	f.control.Disconnect(string(f.run.ID), "tab", lease.Generation)
	f.settled()
	f.wantTyped(t)
	if err := f.control.Release(string(f.run.ID), f.member.ID, "tab", lease.Generation); err != nil {
		t.Fatal(err)
	}
	f.woken()
	f.wantTyped(t, 1)
	if room := f.room(t); len(room) != 0 {
		t.Fatalf("a wake nobody had to be shown left %d room messages", len(room))
	}
}

func TestATypedWakeIsShownToTheMemberInControlThenTyped(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	notice := f.notice(t, 1)[0]
	due := f.now.Load().Add(collab.SteeringDelay)
	if notice.Kind != store.RoomMessageSteerRequest || notice.State != store.RoomMessageQueued || notice.Via != domain.ViaAgentMail ||
		notice.ActorID != f.member.ID || notice.DeliverAfter == nil || !notice.DeliverAfter.Equal(due) ||
		!strings.HasPrefix(notice.Body, "1 unread message from another agent.") {
		t.Fatalf("notice = %+v, want the owner's queued agent mail steer request due at %s", notice, due)
	}
	f.settled()
	f.wantTyped(t)
	if _, err := f.rooms.DeliverDue(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	f.wantTyped(t)

	f.expire(t)
	f.wantTyped(t, 1)
	if sent := f.room(t); len(sent) != 1 || sent[0].State != store.RoomMessageSent || sent[0].DeliveredAt == nil || f.waker.approved != 0 {
		t.Fatalf("room after the 45 s = %+v with %d approved wakes, want the one notice sent unasked", sent, f.waker.approved)
	}
	f.expire(t)
	f.settled()
	f.wantTyped(t, 1)
	if room := f.room(t); len(room) != 1 || f.steers.Load() != 0 {
		t.Fatalf("mail a line was typed for made %d room messages and %d steers", len(room), f.steers.Load())
	}
}

func TestTheMemberInControlTypesAHeldWakeNow(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	notice := f.notice(t, 1)[0]
	result, err := f.rooms.ApproveNow(context.Background(), notice.ID, f.member.ID, "tab", lease.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt != collab.ReceiptSent || result.Message.State != store.RoomMessageSent || result.Message.DecidedBy != f.member.ID || f.waker.approved != 1 {
		t.Fatalf("approval = %+v, %+v, with %d approved wakes", result, result.Message, f.waker.approved)
	}
	f.wantTyped(t, 1)
	f.expire(t)
	f.wantTyped(t, 1)
}

// The paste and its Enter outlast the request that asked for them.
func TestAHeldWakeTypedAsItsRequestIsCancelledIsRecordedSent(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	notice := f.notice(t, 1)[0]
	ctx, cancel := context.WithCancel(context.Background())
	f.waker.mu.Lock()
	f.waker.onType = cancel
	f.waker.mu.Unlock()
	_, err := f.rooms.ApproveNow(ctx, notice.ID, f.member.ID, "tab", lease.Generation)
	if room := f.room(t); err != nil || room[0].State != store.RoomMessageSent {
		t.Fatalf("a typed line whose request was cancelled: ApproveNow = %v, notice %+v; a queued notice is typed again", err, room[0])
	}
	f.expire(t)
	f.wantTyped(t, 1)
}

// No line is typed while the agent prints, so the member in control could
// only dismiss a notice shown then.
func TestMailForAWorkingAgentMakesNoNoticeUntilItsTerminalIsQuiet(t *testing.T) {
	f := newNoticeFixture(t)
	f.waker.printing.Store(true)
	f.takeControl(t)
	f.settled()
	f.wantTyped(t)
	if room := f.room(t); len(room) != 0 {
		t.Fatalf("mail for an agent that is printing made %d notices", len(room))
	}
	f.waker.printing.Store(false)
	f.notice(t, 1)
}

func TestADismissedWakeIsNotTypedAndNewMailMakesANewNotice(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	notice := f.notice(t, 1)[0]
	denied, err := f.rooms.Deny(context.Background(), notice.ID, f.member.ID, "tab", lease.Generation)
	if err != nil || denied.State != store.RoomMessageDenied {
		t.Fatalf("dismissal = %+v, %v", denied, err)
	}
	f.expire(t)
	f.settled()
	f.wantTyped(t)
	if room := f.room(t); len(room) != 1 {
		t.Fatalf("the dismissed mail came back as %d room messages", len(room))
	}

	f.mail(t, "more mail")
	room := f.notice(t, 2)
	if room[0].State != store.RoomMessageQueued || !strings.HasPrefix(room[0].Body, "2 unread messages from other agents.") {
		t.Fatalf("notice for new mail = %+v", room[0])
	}
}

func TestMailDuringAHeldWakeJoinsItsNotice(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	f.notice(t, 1)
	f.mail(t, "more mail")
	f.settled()
	if room := f.room(t); len(room) != 1 {
		t.Fatalf("mail during a held wake made %d notices", len(room))
	}
	f.expire(t)
	f.wantTyped(t, 2)
	f.settled()
	f.wantTyped(t, 2)
}

func TestAHeldWakeWaitsForATerminalThatCannotTakeItsLine(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	notice := f.notice(t, 1)[0]
	refusal := fmt.Errorf("scheduler: %w: a person typed in the terminal in the last 10s", domain.ErrWakeHeld)
	f.waker.refuse(refusal)
	if _, err := f.rooms.ApproveNow(context.Background(), notice.ID, f.member.ID, "tab", lease.Generation); !errors.Is(err, refusal) || !errors.Is(err, collab.ErrWakeNotTyped) {
		t.Fatalf("typing it now into a busy terminal = %v, want the refusal", err)
	}
	f.expire(t)
	f.expire(t)
	if room := f.room(t); room[0].State != store.RoomMessageQueued || room[0].DecidedBy != "" || f.rooms.OverdueDeliveryError() != nil {
		t.Fatalf("a postponed notice = %+v, overdue error %v", room[0], f.rooms.OverdueDeliveryError())
	}
	f.wantTyped(t)

	failure := errors.New("scheduler: typed wake: read the session supervisor's state: exec failed")
	f.waker.refuse(failure)
	if _, err := f.rooms.DeliverDue(context.Background(), 0); !errors.Is(err, failure) {
		t.Fatalf("an overdue notice whose wake failed = %v, want the failure and not a postponement", err)
	}
	if room := f.room(t); room[0].State != store.RoomMessageQueued {
		t.Fatalf("notice whose wake failed = %+v", room[0])
	}

	f.waker.refuse(nil)
	f.expire(t)
	f.wantTyped(t, 1)
	if room := f.room(t); room[0].State != store.RoomMessageSent {
		t.Fatalf("notice after the terminal went quiet = %+v", room[0])
	}
}

// Text a member in control left in the input box is submitted joined to the
// line, which only the terminal's side can tell. A dropped connection keeps
// the lease, and the draft, for the reconnect window.
func TestAHeldWakeTellsTheTerminalAMemberInControlWasShownIt(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	notice := f.notice(t, 1)[0]
	f.waker.refuse(fmt.Errorf("scheduler: %w: the member in control typed and has not pressed Enter", domain.ErrWakeHeld))
	f.expire(t)
	if !f.waker.lastShown() {
		t.Fatal("the overdue wake of a notice was not typed as shown to the member in control, so their draft would not hold it")
	}
	if _, err := f.rooms.ApproveNow(context.Background(), notice.ID, f.member.ID, "tab", lease.Generation); !errors.Is(err, collab.ErrWakeNotTyped) {
		t.Fatalf("typing it now into a refusing terminal = %v", err)
	}
	if f.waker.lastShown() {
		t.Fatal("a wake the member asked for was typed as shown, so their own draft would hold it")
	}
	f.control.Disconnect(string(f.run.ID), "tab", lease.Generation)
	f.expire(t)
	if !f.waker.lastShown() {
		t.Fatal("a dropped connection that still holds the lease lost the member their draft's hold")
	}
	if err := f.control.Release(string(f.run.ID), f.member.ID, "tab", lease.Generation); err != nil {
		t.Fatal(err)
	}
	f.expire(t)
	if f.waker.lastShown() {
		t.Fatal("a wake whose member let go was typed as shown, so a draft they left would hold it for good")
	}
}

// Protection cancels every queued message and takes control away.
func TestAHeldWakeThatProtectionCancelledStaysOwed(t *testing.T) {
	for _, retake := range []bool{false, true} {
		f := newNoticeFixture(t)
		ctx := context.Background()
		f.takeControl(t)
		f.notice(t, 1)
		if err := f.db.GrantWorkspace(ctx, f.run.WorkspaceID, f.member.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := f.rooms.Protect(ctx, f.run.ID, f.member.ID); err != nil {
			t.Fatal(err)
		}
		if room := f.room(t); room[0].State != store.RoomMessageCancelled {
			t.Fatalf("notice of a protected session = %+v", room[0])
		}
		f.settled()
		f.wantTyped(t)
		if err := f.db.SetRunProtected(ctx, f.run.ID, false); err != nil {
			t.Fatal(err)
		}
		if !retake {
			f.woken()
			f.wantTyped(t, 1)
			continue
		}
		f.takeControl(t)
		if room := f.notice(t, 2); room[0].State != store.RoomMessageQueued {
			t.Fatalf("notice after protection ended = %+v", room[0])
		}
		f.expire(t)
		f.wantTyped(t, 1)
	}
}

func TestAHeldWakeLeftByASwitchToEnhancedStaysOwed(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	f.notice(t, 1)
	f.waker.enhanced.Store(true)
	f.expire(t)
	f.wantTyped(t)
	if room := f.room(t); room[0].State != store.RoomMessageNotSent {
		t.Fatalf("notice of a session that takes no typed wake = %+v", room[0])
	}
	f.woken()
	if prompt := protocol.CoordInboxContext(1) + protocol.CoordReportReminder; len(f.waker.typed()) != 1 || f.waker.typed()[0] != prompt {
		t.Fatalf("the Enhanced session was prompted %q, want %q", f.waker.typed(), prompt)
	}
}

// Its parent is told the mail is held, which means until the member lets go.
func TestAControlledSubsessionKeepsItsMailUntilTheMemberLetsGo(t *testing.T) {
	f := newNoticeFixtureFor(t, true)
	lease := f.takeControl(t)
	f.settled()
	f.wantTyped(t)
	if room := f.room(t); len(room) != 0 {
		t.Fatalf("held subsession mail made %d notices", len(room))
	}
	if err := f.control.Release(string(f.run.ID), f.member.ID, "tab", lease.Generation); err != nil {
		t.Fatal(err)
	}
	f.woken()
	f.wantTyped(t, 1)
}

func TestADismissedWakeStaysDismissedAfterAServerRestart(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		f := newNoticeFixture(t)
		lease := f.takeControl(t)
		notice := f.notice(t, 1)[0]
		if _, err := f.rooms.Deny(context.Background(), notice.ID, f.member.ID, "tab", lease.Generation); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.coord.Close(), f.rooms.Close()); err != nil {
			t.Fatal(err)
		}
		f.control = control.New(control.Config{})
		f.boot(t, nil)
		if reconnect {
			f.takeControl(t)
		}
		f.settled()
		f.wantTyped(t)
		if room := f.room(t); len(room) != 1 {
			t.Fatalf("reconnect %v: the dismissed mail came back as %d room messages", reconnect, len(room))
		}
		f.mail(t, "more mail")
		if reconnect {
			f.notice(t, 2)
			continue
		}
		f.woken()
		f.wantTyped(t, 2)
	}
}

func TestAHeldWakeForMailTheAgentReadIsNotTyped(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	f.notice(t, 1)
	f.readMail(t)
	f.expire(t)
	f.wantTyped(t)
	if room := f.room(t); room[0].State != store.RoomMessageNotSent || room[0].Failure == nil || room[0].Failure.Code != "nothing_to_wake" {
		t.Fatalf("notice for read mail = %+v, failure %+v", room[0], room[0].Failure)
	}
}

func TestAHeldWakeSurvivesAServerRestart(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	f.notice(t, 1)
	if err := errors.Join(f.coord.Close(), f.rooms.Close()); err != nil {
		t.Fatal(err)
	}

	f.control = control.New(control.Config{})
	ready := make(chan struct{})
	f.boot(t, ready)
	later := f.now.Load().Add(collab.SteeringDelay + time.Second)
	f.now.Store(&later)
	if err := f.rooms.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	f.wantTyped(t)
	close(ready)
	deadline := time.Now().Add(3 * time.Second)
	for len(f.waker.typed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	f.wantTyped(t, 1)
	f.settled()
	f.wantTyped(t, 1)
	if room := f.room(t); len(room) != 1 || room[0].State != store.RoomMessageSent {
		t.Fatalf("room after the restart = %+v", room)
	}
}

func TestATeammatesHeldMessageIsDeliveredAsBefore(t *testing.T) {
	f := newNoticeFixture(t)
	ctx := context.Background()
	f.readMail(t)
	teammate := &domain.Member{DisplayName: "Teammate", TailnetLogin: "teammate@example.com", Role: domain.RoleAdmin}
	if err := f.db.CreateMember(ctx, teammate); err != nil {
		t.Fatal(err)
	}
	if err := f.db.GrantWorkspace(ctx, f.run.WorkspaceID, teammate.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.takeControl(t)
	post := collab.MessageInput{WorkspaceID: f.run.WorkspaceID, RunID: f.run.ID, ActorID: teammate.ID, Kind: store.RoomMessageSteerRequest, Body: "rebase first", IdempotencyKey: "steer"}
	held, err := f.rooms.Post(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	if held.Message.State != store.RoomMessageQueued || held.Message.Via != "" || f.steers.Load() != 0 {
		t.Fatalf("a teammate's message = %+v with %d steers, want it held", held.Message, f.steers.Load())
	}
	f.expire(t)
	if room := f.room(t); f.steers.Load() != 1 || room[0].State != store.RoomMessageSent {
		t.Fatalf("after the 45 s: %d steers, message %+v", f.steers.Load(), room[0])
	}
	f.wantTyped(t)

	post.IdempotencyKey = "forged"
	if _, err = f.rooms.Post(domain.WithVia(ctx, domain.ViaAgentMail), post); !errors.Is(err, collab.ErrInvalidRequest) {
		t.Fatalf("a post under the agent mail name = %v, want it refused", err)
	}
	if room := f.room(t); len(room) != 1 {
		t.Fatalf("the refused post left %d room messages", len(room))
	}
}

// Control-owned cleanup releases a run's coordination, which waits for the
// wake that would hold the notice, inside the run's admission.
func TestHoldingAWakeNeverWaitsForBusyControl(t *testing.T) {
	f := newNoticeFixture(t)
	f.takeControl(t)
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = f.control.Admit(string(f.run.ID), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	held := make(chan error, 1)
	go func() {
		_, _, err := f.rooms.HoldWake(context.Background(), f.run.ID, "key", "notice")
		held <- err
	}()
	select {
	case err := <-held:
		if !errors.Is(err, control.ErrAdmissionBusy) {
			t.Fatalf("HoldWake during another admission = %v, want it refused as busy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HoldWake waited for the run's admission")
	}
	close(release)
	f.notice(t, 1)
}
