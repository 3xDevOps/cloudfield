package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"testing"
	"time"
)

func TestStepsLogOneLineInOrder(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	Step(context.Background(), "untimed")
	LogSteps(context.Background(), "nothing")
	if out.Len() != 0 {
		t.Fatalf("a context without steps logged %q", out.String())
	}

	ctx := WithSteps(context.Background())
	if WithSteps(ctx) != ctx {
		t.Error("WithSteps replaced the steps a context already times")
	}
	Step(ctx, "first")
	StepAt(ctx, "earlier", time.Now().Add(-time.Hour))
	StepAt(ctx, "second", time.Now().Add(250*time.Millisecond))
	LogSteps(ctx, "launch steps", "run", "run-a")
	want := regexp.MustCompile(`msg="launch steps" run=run-a total_ms=\S+ first_ms=\S+ second_ms=2\d\d\S*\n$`)
	if !want.Match(out.Bytes()) {
		t.Errorf("logged %q, want it to match %s", out.String(), want)
	}
}
