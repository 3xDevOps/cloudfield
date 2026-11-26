package scheduler

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// A restart that finds the continuation recorded as being sent does not
// send it again, since the agent may be working on it: the session keeps
// running and its log says the prompt may not have arrived.
func TestRestartNeverResendsAContinuationBeingSent(t *testing.T) {
	t.Parallel()
	e, rt, _ := newFallbackEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = e.db.SetRunAccount(t.Context(), run.ID, domain.AccountOf(e.owner.ID), usageLimitSegment, time.Now()); err != nil {
		t.Fatal(err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc.AccountSwap = &accountSwap{From: e.member.ID, To: e.owner.ID, Continuation: continuedOnGrace, Sending: true, Container: sc.ContainerID}
	if err = e.sched.writeSidecar(sc); err != nil {
		t.Fatal(err)
	}
	before := len(rt.all())
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	waitFor(t, "the marker cleared", func() bool {
		sc, err := s2.readSidecar(run.ID)
		return err == nil && sc.AccountSwap == nil
	})
	waitFor(t, "the notice", func() bool {
		log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
		return readErr == nil && strings.Contains(string(log), "the continuation prompt may not have been delivered")
	})
	time.Sleep(500 * time.Millisecond)
	for _, exec := range rt.all()[before:] {
		if prompts := exec.wire.prompts(t); len(prompts) != 0 {
			t.Fatalf("the restart sent %d prompts; want the continuation never sent again", len(prompts))
		}
	}
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status.Terminal() {
		t.Fatalf("run = %+v, %v; want it still live", row, err)
	}
}

// Between two logins of one account only a spent balance moves a session.
func TestDepletionMessages(t *testing.T) {
	for text, want := range map[string]bool{
		"You've hit your weekly limit · resets Oct 14, 9am (UTC)":        true,
		"Internal error: You've reached your usage limit":                true,
		"You're out of usage credits":                                    true,
		"Your org is out of usage · contact your admin":                  true,
		"Your seat type doesn't include usage":                           false,
		"Your usage allocation has been disabled by your admin":          false,
		"Your group's usage limit is set to $0":                          false,
		"Server is temporarily limiting requests (not your usage limit)": false,
		"Fable 5 requires usage credits":                                 false,
	} {
		if got := agentstatus.IsDepletionMessage(text); got != want {
			t.Errorf("IsDepletionMessage(%q) = %v, want %v", text, got, want)
		}
	}
}
