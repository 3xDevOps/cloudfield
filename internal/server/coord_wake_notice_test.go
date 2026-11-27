package server

import (
	"context"
	"errors"
	"path/filepath"
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
}

func (*noticeWaker) Wakeable(domain.RunID) bool  { return true }
func (*noticeWaker) InTurn(domain.RunID) bool    { return false }
func (*noticeWaker) TypedWake(domain.RunID) bool { return true }

func (w *noticeWaker) Wake(ctx context.Context, _ domain.RunID, prompt string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return w.refusal
	}
	w.lines = append(w.lines, prompt)
	if domain.WakeApproved(ctx) {
		w.approved++
	}
	return nil
}

func (w *noticeWaker) typed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
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
	f := &noticeFixture{wakeServerFixture: newWakeServerFixture(t, false), waker: &noticeWaker{}}
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

func TestATypedWakeWithNobodyConnectedInControlIsTypedAtOnce(t *testing.T) {
	f := newNoticeFixture(t)
	lease := f.takeControl(t)
	f.control.Disconnect(string(f.run.ID), "tab", lease.Generation)
	f.settled()
	f.wantTyped(t)
	if err := f.control.Release(string(f.run.ID), f.member.ID, "tab", lease.Generation); err != nil {
		t.Fatal(err)
	}
	f.coord.WakeIdle(f.run.ID)
	deadline := time.Now().Add(3 * time.Second)
	for len(f.waker.typed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
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
	refusal := errors.New("scheduler: typed wake held: a person typed in the terminal in the last 10s")
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

	f.waker.refuse(nil)
	f.expire(t)
	f.wantTyped(t, 1)
	if room := f.room(t); room[0].State != store.RoomMessageSent {
		t.Fatalf("notice after the terminal went quiet = %+v", room[0])
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
		_, err := f.rooms.HoldWake(context.Background(), f.run.ID, "key", "notice")
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
