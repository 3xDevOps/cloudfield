package ptyhost

import (
	"context"
	"sync"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// TestOnInputReportsFirstKeystrokeOnce pins the hook the server turns into
// a co-author record: it fires once per write attach, after the keystrokes
// reach the PTY, and never for a mirror.
func TestOnInputReportsFirstKeystrokeOnce(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []domain.MemberID
	)
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.OnInput = func(_ SessionKey, member domain.MemberID) {
			mu.Lock()
			seen = append(seen, member)
			mu.Unlock()
		}
	})
	report := func() []domain.MemberID {
		mu.Lock()
		defer mu.Unlock()
		return append([]domain.MemberID(nil), seen...)
	}

	att := newFakeAtt()
	stdin := att.captureStdin()
	run := domain.RunID("run-input")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	mirror := startAttach(t, h, run, "watcher", 80, 24, true)
	waitAttached(t, h, run, 1)
	mirror.typeKeys(t, "ignored")

	// A pane that only reported focus and answered a device-attributes
	// query has typed nothing, and the bytes still reach the PTY.
	a := startAttach(t, h, run, "bob", 80, 24, false)
	a.typeKeys(t, "\x1b[I\x1b[?1;2c")
	waitFor(t, "reports on stdin", func() bool { return stdin.String() == "\x1b[I\x1b[?1;2c" })
	if got := report(); len(got) != 0 {
		t.Fatalf("terminal reports were recorded as input by %v", got)
	}

	a.typeKeys(t, "ls\r")
	waitFor(t, "keystrokes on stdin", func() bool { return stdin.String() == "\x1b[I\x1b[?1;2cls\r" })
	a.typeKeys(t, "pwd\r")
	waitFor(t, "second keystrokes on stdin", func() bool {
		return stdin.String() == "\x1b[I\x1b[?1;2cls\rpwd\r"
	})
	waitFor(t, "input reported once", func() bool { return len(report()) == 1 })

	a.detach()
	if err := a.wait(t); err != nil {
		t.Fatalf("detach returned %v", err)
	}
	mirror.detach()
	if err := mirror.wait(t); err != nil {
		t.Fatalf("mirror detach returned %v", err)
	}
	if got := report(); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("OnInput calls = %v, want one for bob", got)
	}
}

// A draft in the agent's input box is submitted joined to a line the server
// types, which the typed mail wake must not do to a person still writing.
func TestDraftingFollowsWhatAPersonTypedSinceTheirLastEnter(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("run-draft")
	key := RunSession(run)
	att := newFakeAtt()
	att.captureStdin()
	if err := h.StartSession(context.Background(), key, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "bob", 80, 24, false)
	for _, step := range []struct {
		keys  string
		draft bool
	}{
		{"fix the te", true},
		{"sts\r", false},
		{"line one\x1b\r", true},
		{"\r", false},
		{"half a pro", true},
	} {
		a.typeKeys(t, step.keys)
		// The attach reads this focus report only after it recorded the
		// keys, and a report is nothing a person typed.
		a.typeKeys(t, "\x1b[I")
		if got := !h.Drafting(key).IsZero(); got != step.draft {
			t.Fatalf("after %q: drafting %v, want %v", step.keys, got, step.draft)
		}
	}
	if err := h.Inject(context.Background(), key, "Cloudfield", "", "read your inbox", "\r"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if at := h.Drafting(key); !at.IsZero() {
		t.Fatalf("a line the server submitted left a draft from %s", at)
	}
}
