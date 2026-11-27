package ctxlock

import (
	"context"
	"errors"
	"testing"
)

func TestLockContextGivesUpWithItsCaller(t *testing.T) {
	var m Mutex
	m.Lock()
	left := errors.New("the caller left")
	ctx, leave := context.WithCancelCause(t.Context())
	waited := make(chan error, 1)
	go func() { waited <- m.LockContext(ctx) }()
	leave(left)
	if err := <-waited; !errors.Is(err, left) {
		t.Fatalf("LockContext of a held mutex for a caller that left = %v, want %v", err, left)
	}
	m.Unlock()
	if err := m.LockContext(ctx); !errors.Is(err, left) {
		t.Fatalf("LockContext of a free mutex for a caller that left = %v, want %v", err, left)
	}
	if !m.TryLock() {
		t.Fatal("a wait that gave up left the mutex locked")
	}
	m.Unlock()
	if err := m.LockContext(t.Context()); err != nil {
		t.Fatalf("LockContext of a free mutex = %v", err)
	}
	if m.TryLock() {
		t.Fatal("TryLock took a mutex LockContext holds")
	}
}
