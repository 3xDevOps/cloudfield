package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// awakeExec keeps its run awake until the exec is seen to end.
type awakeExec struct {
	runtime.ManagedExec
	done func()
}

func (s *Scheduler) keepAwake(run domain.RunID, exec runtime.ManagedExec) runtime.ManagedExec {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		s.mu.Unlock()
		return exec
	}
	entry.awake++
	entry.execAt = time.Now().UTC()
	s.mu.Unlock()
	return &awakeExec{ManagedExec: exec, done: sync.OnceFunc(func() {
		s.mu.Lock()
		entry.awake--
		entry.execAt = time.Now().UTC()
		s.mu.Unlock()
	})}
}

func (e *awakeExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	st, err := e.ManagedExec.Wait(ctx)
	if err == nil || errors.Is(err, runtime.ErrNotFound) {
		e.done()
	}
	return st, err
}

func (e *awakeExec) Stop(ctx context.Context, grace time.Duration) (runtime.ExitStatus, error) {
	st, err := e.ManagedExec.Stop(ctx, grace)
	if err == nil {
		e.done()
	}
	return st, err
}

func (e *awakeExec) Detach() error {
	e.done()
	return e.ManagedExec.Detach()
}
