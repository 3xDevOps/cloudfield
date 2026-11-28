package scheduler

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/ctxlock"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	acp "github.com/coder/acp-go-sdk"
)

// A restart that finds the continuation recorded as being sent does not
// send it again, since the agent may be working on it: the session keeps
// running and its log says the prompt may not have arrived. A Background
// session, whose last turn is the one that hit the limit, ends as its log
// says: on the end of a continuation's turn that ended while the server was
// down, or saying the continuation may not have arrived when the log has no
// trace of it. It is never given a new conversation.
func TestRestartNeverResendsAContinuationBeingSent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		mode   domain.LaunchMode
		logged []acphost.Item
		status domain.RunStatus
		reason string
	}{
		{name: "enhanced", mode: domain.LaunchACP},
		{name: "background without a trace", mode: domain.LaunchHeadless, status: domain.RunFailed,
			reason: "the session moved to Grace; the continuation prompt may not have been delivered"},
		{name: "background whose turn ended", mode: domain.LaunchHeadless, status: domain.RunCompleted, logged: []acphost.Item{
			{Kind: acphost.KindTurnStart},
			{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", Text: continuedOnGrace, Complete: true}},
			{Kind: acphost.KindTurnEnd, StopReason: "end_turn"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// Codex runs Background sessions over ACP.
			e, rt, _ := newFallbackEnv(t, func(cfg *Config) {
				enhancedClaude(cfg)
				cfg.Harnesses["codex"] = HarnessSpec{HeadlessArgs: []string{"fake-agent", "-p", "{task}"}, ACPArgs: []string{"acp-mock"}}
			})
			var run *domain.Run
			var err error
			if c.mode == domain.LaunchACP {
				run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
				if err != nil {
					t.Fatal(err)
				}
				waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
				if err = e.sched.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				// The server stops while the move reads usage, after the
				// task's turn ended on the limit.
				e.share(t)
				writeHomeFiles(t, e.ownerHome, ".codex/auth.json")
				endsBackgroundRuns(e.testEnv)
				stopped := make(chan struct{})
				e.sched.UseUsageFallback(UsageFallback{Usage: &readHook{QuotaReader: codexUsage{}, during: func() {
					go func() {
						_ = e.sched.Close()
						close(stopped)
					}()
					<-e.sched.superCtx.Done()
				}}, AuthorizationMu: &ctxlock.Mutex{}})
				run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, acpmock.PromptUsageLimit, "codex", domain.LaunchHeadless)
				if err != nil {
					t.Fatal(err)
				}
				<-stopped
			}
			if _, err = e.db.SetRunAccount(t.Context(), run.ID, domain.AccountOf(e.owner.ID), false, usageLimitSegment, time.Now()); err != nil {
				t.Fatal(err)
			}
			markContinuationSending(t, e.sched, run.ID, e.member.ID, e.owner.ID)
			if len(c.logged) > 0 {
				log, logErr := acphost.OpenLog(e.pty.ItemLogPath(run.ID))
				if logErr != nil {
					t.Fatal(logErr)
				}
				for _, it := range c.logged {
					if logErr = log.Append(&it); logErr != nil {
						t.Fatal(logErr)
					}
				}
				if logErr = log.Close(); logErr != nil {
					t.Fatal(logErr)
				}
			}
			before := len(rt.all())
			s2 := restartOn(t, e.testEnv)
			waitFor(t, "the marker cleared", func() bool {
				sc, err := s2.readSidecar(run.ID)
				return err == nil && sc.AccountSwap == nil
			})
			waitFor(t, "the notice", func() bool {
				log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
				return readErr == nil && strings.Contains(string(log), "the continuation prompt may not have been delivered")
			})
			if c.status != "" {
				if got := e.waitStoreStatus(t, run.ID, c.status); !strings.Contains(got.Reason, c.reason) {
					t.Fatalf("run %s with %q; want %q", got.Status, got.Reason, c.reason)
				}
			} else {
				time.Sleep(500 * time.Millisecond)
				if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status.Terminal() {
					t.Fatalf("run = %+v, %v; want it still live", row, err)
				}
			}
			for _, exec := range rt.all()[before:] {
				if prompts := exec.wire.prompts(t); len(prompts) != 0 {
					t.Fatalf("the restart sent %d prompts; want the continuation never sent again", len(prompts))
				}
				if slices.Contains(exec.agent.Methods(), acp.AgentMethodSessionNew) {
					t.Fatal("the restart started a new conversation")
				}
			}
		})
	}
}

// A Background session whose agent is working on its continuation when the
// server restarts is not ended by the restart: the turn's end ends it.
func TestRestartLetsAContinuationsTurnFinish(t *testing.T) {
	t.Parallel()
	e, _, _ := newFallbackEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{HeadlessArgs: []string{"fake-agent", "-p", "{task}"}, ACPArgs: []string{"acp-mock"}}
	})
	endsBackgroundRuns(e.testEnv)
	writeHomeFiles(t, e.adaHome, ".codex/auth.json")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, acpmock.PromptThink, "codex", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitAgentWorking(t, run.ID)
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	markContinuationSending(t, e.sched, run.ID, e.owner.ID, e.member.ID)
	s2 := restartOn(t, e.testEnv)
	waitFor(t, "the marker cleared", func() bool {
		sc, scErr := s2.readSidecar(run.ID)
		return scErr == nil && sc.AccountSwap == nil
	})
	time.Sleep(300 * time.Millisecond)
	if row, rowErr := e.db.GetRun(t.Context(), run.ID); rowErr != nil || row.Status.Terminal() || !s2.acp.session(run.ID).State().TurnInFlight {
		t.Fatalf("run = %+v, %v; want it live in its turn", row, rowErr)
	}
	if err = s2.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.waitStoreStatus(t, run.ID, domain.RunFailed); !strings.Contains(got.Reason, "cancelled") {
		t.Fatalf("run ended with %q; want the cancelled turn's end", got.Reason)
	}
}

// endsBackgroundRuns makes a Background run's supervisor exit 0 on SIGUSR1
// and 1 on SIGUSR2, as it ends the run.
func endsBackgroundRuns(e *testEnv) {
	e.rt.execHandler = func(id runtime.ID, argv []string) (int, string, error) {
		c, err := e.rt.get(id)
		if err != nil {
			return 1, "", err
		}
		switch strings.Join(argv, " ") {
		case "/bin/sh -c kill -USR1 1":
			c.exitNow(0)
		case "/bin/sh -c kill -USR2 1":
			c.exitNow(1)
		}
		return 0, "", nil
	}
}

// markContinuationSending records, as a stopped server would have, that the
// run's move from from to to was sending its continuation.
func markContinuationSending(t *testing.T, s *Scheduler, run domain.RunID, from, to domain.MemberID) {
	t.Helper()
	sc, err := s.readSidecar(run)
	if err != nil {
		t.Fatal(err)
	}
	sc.UsageLimit = ""
	sc.AccountSwap = &accountSwap{From: from, To: to, Continuation: continuedOnGrace, Sending: true, Container: sc.ContainerID}
	if err = s.writeSidecar(sc); err != nil {
		t.Fatal(err)
	}
}

// restartOn starts the next server process on e's state and runtime.
func restartOn(t *testing.T, e *testEnv) *Scheduler {
	t.Helper()
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	startScheduler(t, s)
	return s
}
