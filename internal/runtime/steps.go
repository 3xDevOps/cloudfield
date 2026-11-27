package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type stepsKey struct{}

// steps is how long each step of one launch or wake took, in order.
type steps struct {
	mu    sync.Mutex
	began time.Time
	last  time.Time
	attrs []any
}

// WithSteps returns a context that times the steps of one launch or wake:
// Step ends a step, LogSteps writes them all as one log line. A context that
// already times steps is returned as it is, so the outermost caller decides
// where the first step begins.
func WithSteps(ctx context.Context) context.Context {
	if ctx.Value(stepsKey{}) != nil {
		return ctx
	}
	now := time.Now()
	return context.WithValue(ctx, stepsKey{}, &steps{began: now, last: now})
}

// Step records name as everything done since the step before it ended. It
// does nothing in a context WithSteps did not make.
func Step(ctx context.Context, name string) {
	StepAt(ctx, name, time.Now())
}

// StepAt is Step for a step that ended at a time learned afterwards, such
// as from a file's modification time. A time before the last step's end
// records nothing.
func StepAt(ctx context.Context, name string, at time.Time) {
	s, _ := ctx.Value(stepsKey{}).(*steps)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.Before(s.last) {
		return
	}
	s.attrs = append(s.attrs, name+"_ms", milliseconds(at.Sub(s.last)))
	s.last = at
}

// LogSteps writes one log line with msg, args, the time since WithSteps as
// total_ms and every step as <name>_ms, in the order they ended. Time after
// the last step is in the total only.
func LogSteps(ctx context.Context, msg string, args ...any) {
	s, _ := ctx.Value(stepsKey{}).(*steps)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	args = append(args, "total_ms", milliseconds(time.Since(s.began)))
	slog.Info(msg, append(args, s.attrs...)...)
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Round(100*time.Microsecond).Microseconds()) / 1000
}
