// Package ctxlock is a mutex a waiter can stop waiting for. The server's
// admission lock and each session's lifecycle lock are held across work
// that can hang, so a request waiting for one must be able to leave when
// its caller does.
package ctxlock

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// Mutex has sync.Mutex's methods and LockContext. Its zero value is
// unlocked, and it must not be copied after first use.
type Mutex struct {
	once sync.Once
	sem  *semaphore.Weighted
}

func (m *Mutex) weighted() *semaphore.Weighted {
	m.once.Do(func() { m.sem = semaphore.NewWeighted(1) })
	return m.sem
}

func (m *Mutex) Lock() { _ = m.weighted().Acquire(context.Background(), 1) }

// LockContext locks m unless ctx ends first, and then returns why it ended
// with m not locked. It never locks m for a ctx that has already ended.
func (m *Mutex) LockContext(ctx context.Context) error {
	if m.weighted().Acquire(ctx, 1) != nil {
		return context.Cause(ctx)
	}
	return nil
}

func (m *Mutex) TryLock() bool { return m.weighted().TryAcquire(1) }

func (m *Mutex) Unlock() { m.weighted().Release(1) }
