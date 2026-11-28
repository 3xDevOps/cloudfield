package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func waitingIntent(t *testing.T, db *DB, w *domain.Workspace, m *domain.Member, key string) *domain.LaunchIntent {
	t.Helper()
	in, existing, err := db.CreateLaunchIntent(context.Background(), &domain.LaunchIntent{
		MemberID: m.ID, WorkspaceID: w.ID, IdempotencyKey: key, Method: "run.launch", Params: []byte(`{"task":"wait"}`),
		Base: domain.LaunchBase{Commit: "0123456789abcdef0123456789abcdef01234567", Branch: "main", Source: "origin", CheckedAt: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)},
		Via:  "chatgpt", Refusal: "no room",
	}, 8)
	if err != nil || existing {
		t.Fatalf("CreateLaunchIntent = %+v, existing %v, %v", in, existing, err)
	}
	return in
}

func intentRun(in *domain.LaunchIntent) *domain.Run {
	return &domain.Run{ID: in.ID, WorkspaceID: in.WorkspaceID, MemberID: in.MemberID, Task: "wait", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued}
}

func TestALaunchIntentKeepsItsRequestAndIsOneToItsKey(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	w, m := mustCreateWorkspace(t, db), mustCreateMember(t, db)
	in := waitingIntent(t, db, w, m, "k-1")
	got, err := db.GetLaunchIntent(ctx, in.ID)
	if err != nil || got.State != domain.IntentWaiting || got.Base != in.Base || string(got.Params) != `{"task":"wait"}` || got.Via != "chatgpt" || got.Refusal != "no room" || got.IdempotencyKey != "k-1" {
		t.Fatalf("stored intent = %+v, %v", got, err)
	}
	again, existing, err := db.CreateLaunchIntent(ctx, &domain.LaunchIntent{MemberID: m.ID, WorkspaceID: w.ID, IdempotencyKey: "k-1", Method: "run.launch", Params: []byte(`{}`)}, 8)
	if err != nil || !existing || again.ID != in.ID {
		t.Fatalf("the same key while it waits = %+v, existing %v, %v; want the same intent", again, existing, err)
	}
	// With no key of its own, its ID is its key.
	keyless := waitingIntent(t, db, w, m, "")
	if keyless.IdempotencyKey != string(keyless.ID) {
		t.Fatalf("a keyless intent's key = %q, want its ID %s", keyless.IdempotencyKey, keyless.ID)
	}
	if _, _, err := db.CreateLaunchIntent(ctx, &domain.LaunchIntent{MemberID: m.ID, WorkspaceID: w.ID, Method: "run.launch", Params: []byte(`{}`)}, 2); !errors.Is(err, ErrTooManyWaiting) {
		t.Fatalf("a third open intent with a cap of two = %v, want ErrTooManyWaiting", err)
	}
	if by, err := db.LaunchIntentByKey(ctx, m.ID, "k-1"); err != nil || by.ID != in.ID {
		t.Fatalf("LaunchIntentByKey = %+v, %v", by, err)
	}
	if list, err := db.LaunchIntents(ctx, "", domain.IntentWaiting); err != nil || len(list) != 2 || list[0].ID != in.ID {
		t.Fatalf("waiting intents = %+v, %v; want both, the oldest first", list, err)
	}
}

// The session of an intent and the intent's start are one write: a session
// exists exactly when its intent is started, and never for one a cancel
// reached first.
func TestAnIntentsSessionAndItsStartAreOneWrite(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	w, m := mustCreateWorkspace(t, db), mustCreateMember(t, db)
	in := waitingIntent(t, db, w, m, "k-1")
	if err := db.CreateRunForIntent(ctx, intentRun(in)); !errors.Is(err, ErrConflict) {
		t.Fatalf("a session for an intent no attempt claimed = %v, want ErrConflict", err)
	}
	now := time.Now()
	if err := db.ClaimLaunchIntent(ctx, in.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimLaunchIntent(ctx, in.ID, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second claim = %v, want ErrConflict: one attempt at a time", err)
	}
	if err := db.CreateRunForIntent(ctx, intentRun(in)); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetLaunchIntent(ctx, in.ID)
	if run, err := db.GetRun(ctx, in.ID); err != nil || run.MemberID != m.ID || got.State != domain.IntentStarted || got.Attempts != 1 {
		t.Fatalf("after the start: session %+v, %v, intent %+v", run, err, got)
	}
	if err := db.CreateRunForIntent(ctx, intentRun(in)); err == nil {
		t.Fatal("a second session for a started intent was created")
	}
	if err := db.EndLaunchIntent(ctx, in.ID, domain.IntentCancelled, "late", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("a cancel after the start = %v, want ErrConflict", err)
	}

	cancelled := waitingIntent(t, db, w, m, "k-2")
	if err := db.ClaimLaunchIntent(ctx, cancelled.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.EndLaunchIntent(ctx, cancelled.ID, domain.IntentCancelled, "cancelled by its member", now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunForIntent(ctx, intentRun(cancelled)); !errors.Is(err, ErrConflict) {
		t.Fatalf("a session for an intent cancelled during its attempt = %v, want ErrConflict", err)
	}
	if _, err := db.GetRun(ctx, cancelled.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("its session = %v, want none: the insert was undone", err)
	}
}

// A server that stopped between an attempt's claim and its end left the
// intent starting. The next server returns it to waiting, or marks it
// started when its session exists.
func TestIntentsLeftStartingAreSettledAfterARestart(t *testing.T) {
	path := templateDBPath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	w, m := mustCreateWorkspace(t, db), mustCreateMember(t, db)
	noSession, withSession, waiting := waitingIntent(t, db, w, m, "k-1"), waitingIntent(t, db, w, m, "k-2"), waitingIntent(t, db, w, m, "k-3")
	for _, in := range []*domain.LaunchIntent{noSession, withSession} {
		if err := db.ClaimLaunchIntent(ctx, in.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// The session row written, the intent's own update lost: what a crash
	// could only leave if the two were not one transaction.
	if err := db.CreateRunWithID(ctx, intentRun(withSession)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.RecoverLaunchIntents(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[domain.RunID]domain.LaunchIntentState{
		noSession.ID: domain.IntentWaiting, withSession.ID: domain.IntentStarted, waiting.ID: domain.IntentWaiting,
	} {
		if got, err := db.GetLaunchIntent(ctx, id); err != nil || got.State != want {
			t.Fatalf("intent %s after the restart = %+v, %v; want %s", id, got, err, want)
		}
	}
	if got, _ := db.GetLaunchIntent(ctx, noSession.ID); got.Refusal != "no room" || got.Attempts != 1 {
		t.Fatalf("the intent returned to waiting = %+v, want its refusal and attempt kept", got)
	}
	// It can be claimed again, and started once.
	if err := db.ClaimLaunchIntent(ctx, noSession.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunForIntent(ctx, intentRun(noSession)); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingIntentsCountTheLaunchesThatPassedThem(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	w, m := mustCreateWorkspace(t, db), mustCreateMember(t, db)
	first, claimed := waitingIntent(t, db, w, m, "k-1"), waitingIntent(t, db, w, m, "k-2")
	if err := db.ClaimLaunchIntent(ctx, claimed.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if n, err := db.PassLaunchIntents(ctx); err != nil || n != 1 {
			t.Fatalf("PassLaunchIntents = %d, %v; want the one waiting", n, err)
		}
	}
	if got, _ := db.GetLaunchIntent(ctx, first.ID); got.Passed != 2 {
		t.Fatalf("passed = %d, want 2", got.Passed)
	}
	if err := db.ReleaseLaunchIntent(ctx, claimed.ID, "still no room"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetLaunchIntent(ctx, claimed.ID); got.State != domain.IntentWaiting || got.Refusal != "still no room" || got.Passed != 0 {
		t.Fatalf("released intent = %+v", got)
	}
}
