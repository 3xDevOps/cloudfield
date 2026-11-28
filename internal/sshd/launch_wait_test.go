package sshd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
	"github.com/3xDevOps/cloudfield/internal/store"
)

var errTestNoRoom = fmt.Errorf("%w: 100 available bytes; reserve 200 bytes", scheduler.ErrMemoryPressure)

type waitEnv struct {
	*testEnv
	t *testing.T
}

func newWaitEnv(t *testing.T) waitEnv {
	e := newTestEnv(t, nil)
	e.runs.admitInto = e.store
	e.setRoom(false)
	return waitEnv{e, t}
}

func (e *testEnv) setRoom(room bool) {
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	e.runs.noRoom = errTestNoRoom
	if room {
		e.runs.noRoom = nil
	}
}

func (e waitEnv) launch(member domain.MemberID, p protocol.RunLaunchParams) (protocol.RunLaunchResult, *protocol.Error) {
	e.t.Helper()
	p.WorkspaceID, p.Harness = string(e.ws.ID), "claude"
	raw, perr := callJSON(e.t, e.testEnv, member, protocol.MethodRunLaunch, p)
	var result protocol.RunLaunchResult
	if perr == nil {
		if err := json.Unmarshal(raw, &result); err != nil {
			e.t.Fatal(err)
		}
	}
	return result, perr
}

func (e waitEnv) launches() int {
	n := 0
	for _, call := range e.runs.Calls() {
		if strings.HasPrefix(call, "launch:") {
			n++
		}
	}
	return n
}

func (e waitEnv) intent(id string) *domain.LaunchIntent {
	e.t.Helper()
	in, err := e.db.GetLaunchIntent(e.t.Context(), domain.RunID(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return in
}

func (e waitEnv) listed(member domain.MemberID) []protocol.LaunchWaiting {
	e.t.Helper()
	raw, perr := callJSON(e.t, e.testEnv, member, protocol.MethodLaunchWaitingList, protocol.LaunchWaitingListParams{})
	if perr != nil {
		e.t.Fatal(perr)
	}
	var out protocol.LaunchWaitingListResult
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Waiting
}

// A launch that asks to wait is kept when the server has no room, answers
// its key with the same waiting launch, and starts once there is room, as
// one session under the ID it was promised, from the base it was asked at.
func TestALaunchWithNoRoomWaitsAndStartsOnce(t *testing.T) {
	e := newWaitEnv(t)
	if _, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "no wait"}); perr == nil || perr.Code != protocol.CodeUnavailable || !strings.Contains(perr.Message, "insufficient host memory headroom") {
		t.Fatalf("a launch that does not ask to wait = %v, want the refusal it always got", perr)
	}
	first, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-1", Wait: true})
	if perr != nil || first.Waiting == nil || first.Run.ID != "" || first.Waiting.State != "waiting" || first.Waiting.Refusal != errTestNoRoom.Error() {
		t.Fatalf("a launch that asks to wait = %+v, %v; want it waiting with the refusal and no session", first, perr)
	}
	if _, err := e.db.GetRun(t.Context(), domain.RunID(first.Waiting.ID)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the reserved session ID before the start = %v, want no session", err)
	}
	tried := e.launches()
	again, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-1", Wait: true})
	if perr != nil || again.Waiting == nil || again.Waiting.ID != first.Waiting.ID {
		t.Fatalf("the same key again = %+v, %v; want the same waiting launch", again, perr)
	}
	// A caller that does not ask to wait never gets a result without a
	// session: it gets the refusal, and no second launch.
	if _, perr = e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-1"}); perr == nil || perr.Code != protocol.CodeUnavailable ||
		!strings.Contains(perr.Message, errTestNoRoom.Error()) || !strings.Contains(perr.Message, "is waiting for room") {
		t.Fatalf("the same key without wait = %v, want the refusal and that it waits", perr)
	}
	if e.launches() != tried {
		t.Fatalf("a retry of a waiting launch was tried %d more times, want none", e.launches()-tried)
	}
	if _, perr = e.launch(e.member.ID, protocol.RunLaunchParams{Task: "another", IdempotencyKey: "k-1", Wait: true}); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("the key with another task = %v, want a conflict", perr)
	}
	if list := e.listed(e.member.ID); len(list) != 1 || list[0].ID != first.Waiting.ID || list[0].Task != "wait" || list[0].IdempotencyKey != "k-1" {
		t.Fatalf("waiting launches listed = %+v", list)
	}

	// Still no room: it stays, with the latest refusal.
	created := e.intent(first.Waiting.ID).CreatedAt
	e.srv.startWaitingLaunches(t.Context(), created.Add(launchRetryEvery))
	if in := e.intent(first.Waiting.ID); in.State != domain.IntentWaiting || in.Attempts != 1 {
		t.Fatalf("after an attempt with no room = %+v, want it waiting, tried once", in)
	}

	e.setRoom(true)
	e.srv.startWaitingLaunches(t.Context(), created.Add(2*launchRetryEvery))
	in := e.intent(first.Waiting.ID)
	run, err := e.db.GetRun(t.Context(), in.ID)
	if err != nil || in.State != domain.IntentStarted || run.MemberID != e.member.ID || run.Task != "wait" || run.BaseCommit != in.Base.Commit {
		t.Fatalf("once there is room: intent %+v, session %+v, %v; want it started as that session from the kept base", in, run, err)
	}
	receipt, _, err := e.db.LaunchReceipt(t.Context(), e.member.ID, "k-1")
	if err != nil || receipt != in.ID {
		t.Fatalf("the key's receipt after the start = %q, %v; want the session", receipt, err)
	}
	counts, err := e.db.CapacityCounts(t.Context(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	counted := map[string]int64{}
	for _, c := range counts {
		counted[c.Kind+" "+c.Op+" "+c.Cause] += c.Count
	}
	if counted["wait launch "] != 1 || counted["wait-end launch started"] != 1 || len(counted) != 2 {
		t.Fatalf("counted = %v, want one wait and one wait that ended in a start", counted)
	}
	opts := e.runs.launchOptions[len(e.runs.launchOptions)-1]
	if !opts.Intent || opts.ReservedRunID != in.ID || !opts.Retry || opts.Queue || opts.Base == nil {
		t.Fatalf("the start's options = %+v, want the intent's ID, its base, and a retry", opts)
	}
	tried = e.launches()
	replay, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-1", Wait: true})
	if perr != nil || replay.Waiting != nil || replay.Run.ID != string(in.ID) || !replay.Replayed || e.launches() != tried {
		t.Fatalf("the key after the start = %+v, %v; want the session, replayed, and no second launch", replay, perr)
	}
	e.srv.startWaitingLaunches(t.Context(), created.Add(3*launchRetryEvery))
	if e.launches() != tried {
		t.Fatal("a started launch was tried again")
	}
	// The request is the session's now: the intent keeps none of it.
	if string(in.Params) != "" {
		t.Fatalf("a started launch still stores its request: %s", in.Params)
	}
}

// A launch asked for without a key offers none, and its ID is no key: a
// request that names it launches on its own.
func TestAKeylessWaitingLaunchAnswersToNoKey(t *testing.T) {
	e := newWaitEnv(t)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "no key", Wait: true})
	if perr != nil || w.Waiting == nil || w.Waiting.IdempotencyKey != "" {
		t.Fatalf("a keyless waiting launch = %+v, %v; want it waiting and offering no key", w, perr)
	}
	other, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "another task", IdempotencyKey: w.Waiting.ID, Wait: true})
	if perr != nil || other.Waiting == nil || other.Waiting.ID == w.Waiting.ID {
		t.Fatalf("a request keyed with that launch's ID = %+v, %v; want a launch of its own", other, perr)
	}
	e.setRoom(true)
	e.srv.startWaitingLaunches(t.Context(), time.Now().Add(launchRetryEvery))
	for _, id := range []string{w.Waiting.ID, other.Waiting.ID} {
		if in := e.intent(id); in.State != domain.IntentStarted {
			t.Fatalf("launch %s once there is room = %+v, want it started", id, in)
		}
	}
}

// Every check a fresh launch makes is made again when a waiting launch is
// tried: one whose member may no longer launch ends refused with that
// error, and the one behind it is still tried.
func TestAWaitingLaunchIsCheckedAgainWhenItStarts(t *testing.T) {
	e := newWaitEnv(t)
	_, member := addMember(t, e.testEnv, "Grace", domain.RoleCollaborator, false)
	lost, perr := e.launch(member.ID, protocol.RunLaunchParams{Task: "by a member who loses the right", Wait: true})
	if perr != nil || lost.Waiting == nil {
		t.Fatalf("launch = %+v, %v", lost, perr)
	}
	kept, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "behind it", Wait: true})
	if perr != nil || kept.Waiting == nil {
		t.Fatalf("launch = %+v, %v", kept, perr)
	}
	if _, err := e.db.SetMemberRole(t.Context(), member.ID, domain.RoleViewer, ""); err != nil {
		t.Fatal(err)
	}
	e.setRoom(true)
	e.srv.startWaitingLaunches(t.Context(), time.Now().Add(launchRetryEvery))
	in := e.intent(lost.Waiting.ID)
	if in.State != domain.IntentRefused || in.Refusal == "" || in.Refusal == errTestNoRoom.Error() {
		t.Fatalf("a waiting launch of a member who may no longer launch = %+v, want it refused with that error", in)
	}
	if _, err := e.db.GetRun(t.Context(), in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("its session = %v, want none", err)
	}
	if list := e.listed(member.ID); len(list) != 1 || list[0].State != "refused" || list[0].Refusal != in.Refusal {
		t.Fatalf("what its member sees = %+v, want the refused launch with the error", list)
	}
	if got := e.intent(kept.Waiting.ID); got.State != domain.IntentStarted {
		t.Fatalf("the launch behind it = %+v, want it started", got)
	}
}

// A cancelled launch never starts, its key never launches again, and only
// its member or a super admin may cancel it.
func TestAWaitingLaunchIsCancelled(t *testing.T) {
	e := newWaitEnv(t)
	_, member := addMember(t, e.testEnv, "Grace", domain.RoleCollaborator, false)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-c", Wait: true})
	if perr != nil || w.Waiting == nil {
		t.Fatalf("launch = %+v, %v", w, perr)
	}
	cancel := protocol.LaunchWaitingCancelParams{ID: w.Waiting.ID}
	if _, perr := callJSON(t, e.testEnv, member.ID, protocol.MethodLaunchWaitingCancel, cancel); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("another member's cancel = %v, want not found", perr)
	}
	if _, perr := callJSON(t, e.testEnv, e.member.ID, protocol.MethodLaunchWaitingCancel, cancel); perr != nil {
		t.Fatal(perr)
	}
	e.setRoom(true)
	tried := e.launches()
	e.srv.startWaitingLaunches(t.Context(), time.Now().Add(launchRetryEvery))
	if in := e.intent(w.Waiting.ID); in.State != domain.IntentCancelled || e.launches() != tried {
		t.Fatalf("a cancelled launch = %+v, tried %d more times; want it cancelled and never tried", in, e.launches()-tried)
	}
	if _, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-c", Wait: true}); perr == nil || perr.Code != protocol.CodeConflict || !strings.Contains(perr.Message, "cancelled") {
		t.Fatalf("the key of a cancelled launch = %v, want a conflict that says so", perr)
	}
	if list := e.listed(e.member.ID); len(list) != 0 {
		t.Fatalf("waiting launches after the cancel = %+v, want none", list)
	}
}

// A launch that found no room for a day ends with its last refusal, and
// its member still sees why.
func TestAWaitingLaunchExpiresAfterADay(t *testing.T) {
	e := newWaitEnv(t)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", Wait: true})
	if perr != nil || w.Waiting == nil {
		t.Fatalf("launch = %+v, %v", w, perr)
	}
	tried := e.launches()
	e.srv.startWaitingLaunches(t.Context(), e.intent(w.Waiting.ID).CreatedAt.Add(launchWaitLimit))
	in := e.intent(w.Waiting.ID)
	if in.State != domain.IntentExpired || !strings.Contains(in.Refusal, errTestNoRoom.Error()) || e.launches() != tried {
		t.Fatalf("after a day = %+v, want it expired with the last refusal and not tried", in)
	}
	if list := e.listed(e.member.ID); len(list) != 1 || list[0].State != "expired" {
		t.Fatalf("what its member sees = %+v, want the expired launch", list)
	}
}

// One member cannot fill the queue: past the cap a launch gets the refusal
// it always got.
func TestWaitingLaunchesAreCappedPerMember(t *testing.T) {
	e := newWaitEnv(t)
	for i := range maxWaitingLaunches {
		if w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: fmt.Sprintf("wait %d", i), Wait: true}); perr != nil || w.Waiting == nil {
			t.Fatalf("launch %d = %+v, %v", i, w, perr)
		}
	}
	if _, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "one too many", Wait: true}); perr == nil || perr.Code != protocol.CodeUnavailable ||
		!strings.Contains(perr.Message, "insufficient host memory headroom") || !strings.Contains(perr.Message, "was not kept to wait") || !strings.Contains(perr.Message, "8 launches waiting") {
		t.Fatalf("a launch past the cap = %v, want the refusal", perr)
	}
	_, member := addMember(t, e.testEnv, "Grace", domain.RoleCollaborator, false)
	if w, perr := e.launch(member.ID, protocol.RunLaunchParams{Task: "another member", Wait: true}); perr != nil || w.Waiting == nil {
		t.Fatalf("another member's launch = %+v, %v; want it waiting", w, perr)
	}
}

// A launch asked for through a connector that was removed while it waited
// does not start.
func TestAWaitingLaunchOfARemovedConnectorDoesNotStart(t *testing.T) {
	e := newWaitEnv(t)
	in, _, err := e.db.CreateLaunchIntent(t.Context(), &domain.LaunchIntent{
		MemberID: e.member.ID, WorkspaceID: e.ws.ID, Method: protocol.MethodRunLaunch,
		Params: []byte(fmt.Sprintf(`{"workspace_id":%q,"harness":"claude","task":"from a connector"}`, e.ws.ID)),
		Via:    "ChatGPT", Connector: "con-removed", Refusal: errTestNoRoom.Error(),
	}, maxWaitingLaunches, maxDailyWaiting)
	if err != nil {
		t.Fatal(err)
	}
	e.setRoom(true)
	tried := e.launches()
	e.srv.startWaitingLaunches(t.Context(), time.Now().Add(launchRetryEvery))
	got := e.intent(string(in.ID))
	if got.State != domain.IntentRefused || !strings.Contains(got.Refusal, "connector") || e.launches() != tried {
		t.Fatalf("a waiting launch whose connector was removed = %+v, tried %d times; want it refused untried", got, e.launches()-tried)
	}
}

// A launch asked for from an edge device that no longer admits a
// connection does not start.
func TestAWaitingLaunchOfARevokedDeviceDoesNotStart(t *testing.T) {
	e := newWaitEnv(t)
	in, _, err := e.db.CreateLaunchIntent(t.Context(), &domain.LaunchIntent{
		MemberID: e.member.ID, WorkspaceID: e.ws.ID, Method: protocol.MethodRunLaunch,
		Params: []byte(fmt.Sprintf(`{"workspace_id":%q,"harness":"claude","task":"from a device"}`, e.ws.ID)),
		Device: "dev-gone", Refusal: errTestNoRoom.Error(),
	}, maxWaitingLaunches, maxDailyWaiting)
	if err != nil {
		t.Fatal(err)
	}
	e.setRoom(true)
	tried := e.launches()
	e.srv.startWaitingLaunches(t.Context(), time.Now().Add(launchRetryEvery))
	if got := e.intent(string(in.ID)); got.State != domain.IntentRefused || !strings.Contains(got.Refusal, "device") || e.launches() != tried {
		t.Fatalf("a waiting launch whose device is gone = %+v, tried %d times; want it refused untried", got, e.launches()-tried)
	}
}

// A try that fails on something other than a refusal, such as a database
// read, leaves the launch waiting; and a launch a try left claimed is
// waiting again at the next pass.
func TestAWaitingLaunchSurvivesAFailedTry(t *testing.T) {
	e := newWaitEnv(t)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", Wait: true})
	if perr != nil || w.Waiting == nil {
		t.Fatalf("launch = %+v, %v", w, perr)
	}
	e.runs.mu.Lock()
	e.runs.noRoom, e.runs.err = nil, errors.New("database is locked")
	e.runs.mu.Unlock()
	now := time.Now().Add(launchRetryEvery)
	e.srv.startWaitingLaunches(t.Context(), now)
	if in := e.intent(w.Waiting.ID); in.State != domain.IntentWaiting || in.Failures != 1 || !strings.Contains(in.Refusal, "database is locked") {
		t.Fatalf("after a try that failed on a database error = %+v, want it waiting with the error and one failure counted", in)
	}
	if err := e.db.ClaimLaunchIntent(t.Context(), domain.RunID(w.Waiting.ID), now); err != nil {
		t.Fatal(err)
	}
	e.runs.mu.Lock()
	e.runs.err = nil
	e.runs.mu.Unlock()
	e.srv.startWaitingLaunches(t.Context(), now.Add(launchRetryEvery))
	if in := e.intent(w.Waiting.ID); in.State != domain.IntentStarted {
		t.Fatalf("a launch left claimed, at the next pass = %+v, want it tried and started", in)
	}
}

// A launch a super admin cancels is still shown to its member, with who
// cancelled it.
func TestALaunchCancelledBySomeoneElseStaysVisible(t *testing.T) {
	scoped := newScopedTestEnv(t, nil)
	scoped.runs.admitInto = scoped.store
	scoped.setRoom(false)
	e := waitEnv{scoped, t}
	_, admin := addMember(t, e.testEnv, "Root", domain.RoleSuperAdmin, false)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", Wait: true})
	if perr != nil || w.Waiting == nil {
		t.Fatalf("launch = %+v, %v", w, perr)
	}
	if _, perr := callJSON(t, e.testEnv, admin.ID, protocol.MethodLaunchWaitingCancel, protocol.LaunchWaitingCancelParams{ID: w.Waiting.ID}); perr != nil {
		t.Fatal(perr)
	}
	if list := e.listed(e.member.ID); len(list) != 1 || list[0].State != "cancelled" || !strings.Contains(list[0].Refusal, string(admin.ID)) {
		t.Fatalf("what the member sees after a super admin's cancel = %+v, want the cancelled launch and who cancelled it", list)
	}
}

// A launch whose tries keep failing on something that is not room ends
// refused with that error after a few, not after a day as if for room.
func TestAWaitingLaunchThatKeepsFailingEndsRefused(t *testing.T) {
	e := newWaitEnv(t)
	w, perr := e.launch(e.member.ID, protocol.RunLaunchParams{Task: "wait", Wait: true})
	if perr != nil || w.Waiting == nil {
		t.Fatalf("launch = %+v, %v", w, perr)
	}
	e.runs.mu.Lock()
	e.runs.noRoom, e.runs.err = nil, errors.New("repo extras: origin is gone")
	e.runs.mu.Unlock()
	now := time.Now()
	for i := range maxLaunchFailures {
		if in := e.intent(w.Waiting.ID); in.State != domain.IntentWaiting {
			t.Fatalf("before failing try %d = %+v, want it still waiting", i+1, in)
		}
		now = now.Add(launchRetryEvery)
		e.srv.startWaitingLaunches(t.Context(), now)
	}
	if in := e.intent(w.Waiting.ID); in.State != domain.IntentRefused || !strings.Contains(in.Refusal, "origin is gone") || string(in.Params) != "" || in.Task != "wait" {
		t.Fatalf("after %d failing tries = %+v, want it refused with the error, its request dropped and its task kept", maxLaunchFailures, in)
	}
}

// The assistant's launches do not wait: what asked for one is not there to
// check again when it would start.
func TestAnAssistantLaunchDoesNotWait(t *testing.T) {
	e := newWaitEnv(t)
	raw, err := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Harness: "claude", Task: "from the assistant", Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := e.srv.AssistantLocal(e.member.ID).Call(t.Context(), protocol.MethodRunLaunch, raw); perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("an assistant launch with no room = %v, want the refusal", perr)
	}
	if list := e.listed(e.member.ID); len(list) != 0 {
		t.Fatalf("waiting launches after an assistant launch = %+v, want none", list)
	}
}
