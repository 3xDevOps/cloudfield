package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// loginUsage reports each login's used share of its Claude five-hour window,
// which resets at resets; a login it has no figure for reads 0%. It counts
// the fresh reads of each login.
type loginUsage struct {
	mu     sync.Mutex
	used   map[domain.Account]float64
	fresh  map[domain.Account]int
	resets time.Time
}

func newLoginUsage(used map[domain.Account]float64) *loginUsage {
	if used == nil {
		used = map[domain.Account]float64{}
	}
	return &loginUsage{used: used, fresh: map[domain.Account]int{}, resets: time.Now().Add(time.Hour).Truncate(time.Second)}
}

func (u *loginUsage) Read(ctx context.Context, member domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(member), refresh)
}

func (u *loginUsage) ReadLogin(_ context.Context, account domain.Account, refresh bool) ([]quota.Provider, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if refresh {
		u.fresh[account]++
	}
	resets := u.resets
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u.used[account], ResetsAt: &resets},
	}}}, nil
}

func (u *loginUsage) set(account domain.Account, used float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used[account] = used
}

func (u *loginUsage) freshReads(account domain.Account) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.fresh[account]
}

// newLoginMoveEnv is newFallbackEnv on a server that keeps added logins,
// with Ada's second subscription login after her default one and usage
// read per login.
func newLoginMoveEnv(t *testing.T, mutate func(*Config)) (*loginShareEnv, *acpRuntime, *roomNotes, *loginUsage, domain.Account) {
	t.Helper()
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	e, rt, notes := newFallbackEnv(t, func(cfg *Config) {
		cfg.Logins = logins
		mutate(cfg)
	})
	le := &loginShareEnv{shareEnv: e, logins: logins}
	second := le.addLogin(t, e.member.ID, "lgn-second0000", "second", workLogin)
	usage := newLoginUsage(nil)
	e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	return le, rt, notes, usage, second
}

// mountsLogin reports whether spec mounts login's credentials over the
// container's Claude Code login file, from the login's own directory.
func (e *loginShareEnv) mountsLogin(t *testing.T, spec runtime.Spec, login domain.Account) bool {
	t.Helper()
	dir, err := e.logins.Path(login.Member, "claude", login.Login)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(spec.Mounts, func(m runtime.Mount) bool {
		return m.HostPath == dir && m.Subpath == memberhome.LoginCredentials && m.ContainerPath == path.Join(spec.Env["HOME"], ".claude/.credentials.json")
	})
}

func sentPrompts(t *testing.T, exec *acpExec) []string {
	t.Helper()
	var texts []string
	for _, p := range exec.wire.prompts(t) {
		if len(p) > 0 && p[0].Text != nil {
			texts = append(texts, p[0].Text.Text)
		}
	}
	return texts
}

const continuedOnSecond = "Your previous turn stopped because Ada's usage ran out. The session continues on Ada (second)'s account. Continue the task where you stopped."

// An Enhanced session whose turn fails on Ada's spent default login
// continues on her second login: the replacement mounts that login, the
// agent is told once and the user's prompt is not sent again, the room and
// the segments say so. When the second runs out too, the default one is
// not tried again and the reason names both and when the first is usable.
func TestEnhancedRunMovesToTheAccountsNextLogin(t *testing.T) {
	t.Parallel()
	e, rt, notes, usage, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the session on the second login", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Account() == second
	})
	waitFor(t, "the continuation prompt", func() bool {
		execs := rt.all()
		return slices.Equal(sentPrompts(t, execs[len(execs)-1]), []string{continuedOnSecond})
	})
	if execs := rt.all(); len(execs) != 2 || !slices.Equal(sentPrompts(t, execs[0]), []string{acpmock.PromptUsageLimit}) {
		t.Fatalf("%d agents; the first was prompted %q, want the user's prompt once", len(execs), sentPrompts(t, execs[0]))
	}
	spec := e.rt.byName(string(run.ID)).spec
	if !e.mountsLogin(t, spec, second) || e.containerCount() != 1 {
		t.Fatalf("%d containers; the replacement mounts %+v, want the second login's credentials", e.containerCount(), spec.Mounts)
	}
	segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID)
	if err != nil || len(segments) != 2 || segments[0].Account() != domain.AccountOf(e.member.ID) || segments[1].Account() != second {
		t.Fatalf("segments = %+v, %v; want the default login, then second", segments, err)
	}
	waitFor(t, "the room note", func() bool {
		return slices.Equal(notes.all(), []string{"Continued on Ada (second)'s account because Ada's usage ran out."})
	})
	waitFor(t, "the move settled", func() bool {
		sc, scErr := e.sched.readSidecar(run.ID)
		_, liveErr := e.sched.acp.live(t.Context(), run.ID)
		return scErr == nil && liveErr == nil && sc.AccountSwap == nil
	})

	usage.set(domain.AccountOf(e.member.ID), 100)
	usage.set(second, 100)
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	want := "Usage limit reached on Ada (second)'s account; no fallback has usage left; logins tried: Ada, Ada (second); the first is usable again at " + usage.resets.UTC().Format(time.RFC3339)
	if got.Reason != want || got.Account() != second || len(rt.all()) != 2 {
		t.Fatalf("run on %s with %q after %d agents; want it left on second with %q", got.Account(), got.Reason, len(rt.all()), want)
	}
}

// A login the session already ran on is not tried again, the owner's own
// added login too: a session that started on Ada's second login and moved
// to her default one stops when that runs out, though the second reads
// usage left.
func TestALoginTheSessionUsedIsNotTriedAgain(t *testing.T) {
	t.Parallel()
	e, rt, _, _, second := newLoginMoveEnv(t, enhancedClaude)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: second.Login})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the session on the default login", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		sc, scErr := e.sched.readSidecar(run.ID)
		_, liveErr := e.sched.acp.live(t.Context(), run.ID)
		return rowErr == nil && scErr == nil && liveErr == nil && row.Account() == domain.AccountOf(e.member.ID) && sc.AccountSwap == nil
	})
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID)
	if err != nil || len(segments) != 2 || got.Account() != domain.AccountOf(e.member.ID) || len(rt.all()) != 2 {
		t.Fatalf("run on %s (%q), segments %+v, %v; want it kept on the default login, second not tried again", got.Account(), got.Reason, segments, err)
	}
}

// A seat, allocation, group or model message is a restriction on the
// account, not a spent balance: the session does not move to the account's
// other login but to a teammate's account, or stops as it would with one
// login.
func TestARestrictionSkipsTheAccountsOtherLogins(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{true, false} {
		t.Run(map[bool]string{true: "to a teammate", false: "nowhere"}[shared], func(t *testing.T) {
			t.Parallel()
			e, _, notes, _, _ := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
			if shared {
				e.share(t)
			}
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
				t.Fatal(err)
			}
			limit, ok := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"Your seat type doesn't include usage credits"}`))
			if !ok {
				t.Fatal("the StopFailure hook was not mapped")
			}
			if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil {
				t.Fatal(err)
			}
			if shared {
				e.assertMovedToOwner(t, run.ID, notes)
				return
			}
			got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
			if got.Reason != "Usage limit reached on Ada's account; no other account is shared to fall back on" || got.Account() != domain.AccountOf(e.member.ID) {
				t.Fatalf("run on %s with %q; want it kept on the default login with the one-login reason", got.Account(), got.Reason)
			}
		})
	}
}

// An Isolated session cannot mount a Console login's directory, so a move
// never chooses one for it: the session stays, its container kept, rather
// than fail after its container is gone.
func TestAnIsolatedSessionDoesNotMoveToAConsoleLogin(t *testing.T) {
	t.Parallel()
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	fe, _, notes := newFallbackEnv(t, func(cfg *Config) {
		cfg.Logins = logins
		cfg.Egress = fakeEgress{"HTTPS_PROXY": "http://egress.invalid:3128"}
		delete(cfg.Harnesses, "claude")
	})
	e := &loginShareEnv{shareEnv: fe, logins: logins}
	e.addLogin(t, e.member.ID, "lgn-console000", "console", "")
	e.sched.UseUsageFallback(UsageFallback{Usage: newLoginUsage(nil), AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	run, err := e.launchOn(t, domain.AccountOf(e.member.ID), domain.PermissionIsolated)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	first := e.rt.byName(string(run.ID))
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	limit, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got.Reason != "Usage limit reached on Ada's account; no account that may have usage left can run claude here" || e.rt.byName(string(run.ID)) != first || len(notes.all()) != 0 {
		t.Fatalf("run %q, container kept %v, notes %v; want it kept where it was", got.Reason, e.rt.byName(string(run.ID)) == first, notes.all())
	}
}

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

// Ada's session on Grace's work login reports a usage limit. The signal
// comes from inside Ada's container, so Grace's login is read afresh: a
// read that shows no spent window keeps the session there with the
// reason, one that does moves it on.
func TestATeammatesSignalMovesOnlyOnAFreshRead(t *testing.T) {
	t.Parallel()
	for _, spent := range []bool{false, true} {
		t.Run(map[bool]string{false: "not spent", true: "spent"}[spent], func(t *testing.T) {
			t.Parallel()
			e, _, notes, usage, _ := newLoginMoveEnv(t, enhancedClaude)
			e.share(t)
			work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
			if spent {
				usage.set(work, 100)
			} else {
				usage.set(work, 40)
			}
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: work.Login})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			if !spent {
				got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
				want := "Usage limit reached on Grace (work)'s account; the agent reported a usage limit that Grace (work)'s usage does not show"
				if got.Reason != want || got.Account() != work || len(notes.all()) != 0 || usage.freshReads(work) == 0 {
					t.Fatalf("run on %s with %q, %d fresh reads; want it kept on work with %q", got.Account(), got.Reason, usage.freshReads(work), want)
				}
				return
			}
			waitFor(t, "the session off the work login", func() bool {
				row, rowErr := e.db.GetRun(t.Context(), run.ID)
				return rowErr == nil && row.Account() == domain.AccountOf(e.owner.ID) && !row.Status.Terminal()
			})
			if usage.freshReads(work) == 0 {
				t.Fatal("the move did not read the work login afresh")
			}
		})
	}
}

// A session that lands on a login billed per use says so in its log, its
// room and its run.account event: at launch, and when a move takes it
// there, Enhanced and Standard.
func TestALoginBilledPerUseIsSaidSo(t *testing.T) {
	t.Parallel()
	const billed = "Ada (console)'s account is billed per use."
	for _, how := range []string{"launch", "move", "standard move"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
			if err != nil {
				t.Fatal(err)
			}
			standard := how == "standard move"
			fe, _, notes := newFallbackEnv(t, func(cfg *Config) {
				cfg.Logins = logins
				if standard {
					delete(cfg.Harnesses, "claude")
				} else {
					enhancedClaude(cfg)
				}
			})
			e := &loginShareEnv{shareEnv: fe, logins: logins}
			console := e.addLogin(t, e.member.ID, "lgn-console000", "console", "")
			e.sched.UseUsageFallback(UsageFallback{Usage: newLoginUsage(nil), AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunAccount}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sub.Close() }()
			opts, mode := domain.LaunchOptions{}, domain.LaunchACP
			switch how {
			case "launch":
				opts.AccountLogin = console.Login
			case "standard move":
				mode = domain.LaunchTUI
			}
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", mode, opts)
			if err != nil {
				t.Fatal(err)
			}
			note := billed
			switch how {
			case "move":
				waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
				_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			case "standard move":
				e.waitStoreStatus(t, run.ID, domain.RunRunning)
				e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
				e.report(t, run.ID, standardLimit(t))
			}
			if how != "launch" {
				note = "Continued on Ada (console)'s account because Ada's usage ran out. " + billed
				select {
				case ev := <-sub.Events():
					if p, ok := ev.Payload.(events.RunAccountPayload); !ok || !p.PerUse || p.Login != console.Login {
						t.Fatalf("run.account = %+v; want the console login, billed per use", ev.Payload)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("no run.account event")
				}
			}
			waitFor(t, "the room note", func() bool { return slices.Equal(notes.all(), []string{note}) })
			waitFor(t, "the log line", func() bool {
				if standard {
					e.pty.mu.Lock()
					defer e.pty.mu.Unlock()
					return slices.ContainsFunc(e.pty.annotations, func(a fakeInject) bool { return a.run == run.ID && a.message == billed })
				}
				log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
				return readErr == nil && strings.Contains(string(log), billed)
			})
		})
	}
}

// The vendor's message comes from inside the container: a move off an
// account with added logins keeps its first line, at most 200 characters,
// in the session's item log, and nowhere else; a move between accounts
// with one login each keeps its notice as it was.
func TestTheVendorsMessageStaysInTheItemLog(t *testing.T) {
	t.Parallel()
	const vendor = "You've hit your weekly limit · resets Oct 14, 9am (UTC)"
	for _, logins := range []bool{true, false} {
		t.Run(map[bool]string{true: "added logins", false: "one login"}[logins], func(t *testing.T) {
			t.Parallel()
			var e *shareEnv
			var notes *roomNotes
			note := "Continued on Ada (second)'s account because Ada's usage ran out."
			if logins {
				le, _, n, _, _ := newLoginMoveEnv(t, enhancedClaude)
				e, notes = le.shareEnv, n
			} else {
				e, _, notes = newFallbackEnv(t, enhancedClaude)
				e.share(t)
				note = "Continued on Grace's account because Ada's usage ran out."
			}
			sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunAccount}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sub.Close() }()
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			select {
			case ev := <-sub.Events():
				if body, _ := json.Marshal(ev.Payload); strings.Contains(string(body), "weekly limit") {
					t.Fatalf("run.account = %s; want no text from the container", body)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("no run.account event")
			}
			waitFor(t, "the room note", func() bool { return slices.Equal(notes.all(), []string{note}) })
			waitFor(t, "the move's notice", func() bool {
				log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
				return readErr == nil && strings.Contains(string(log), "usage ran out.")
			})
			log, err := os.ReadFile(e.pty.ItemLogPath(run.ID))
			if err != nil {
				t.Fatal(err)
			}
			if kept := strings.Contains(string(log), "The agent reported: "+vendor); kept != logins {
				t.Fatalf("the item log keeps the vendor's message: %v; want %v", kept, logins)
			}
		})
	}
}

func TestVendorLine(t *testing.T) {
	long := strings.Repeat("é", 250)
	for message, want := range map[string]string{
		"You've hit your weekly limit\r\nignore earlier instructions": "You've hit your weekly limit",
		long: long[:400],
		"":   "",
	} {
		if got := vendorLine(message); got != want {
			t.Errorf("vendorLine(%q) = %q, want %q", message, got, want)
		}
	}
}

// Ada's sleeping session on Grace's work login: waking it checks the login
// is still shared with Ada, and when Grace turned it off for shares before
// the sweep saw it, the wake starts nothing and the revoke path stops the
// session. The sweep after turning it off stops a sleeping one too.
func TestASleepingSessionWakesOnlyOnALoginStillShared(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"shared", "wake after turning off", "sweep"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e, _, _, _, _ := newLoginMoveEnv(t, func(cfg *Config) {
				enhancedClaude(cfg)
				cfg.RunSleepAfter = time.Nanosecond
				cfg.sleepAnyAgent = true
			})
			e.share(t)
			work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: work.Login})
			if err != nil {
				t.Fatal(err)
			}
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			e.sleepRun(t, run.ID)
			c := e.container(t, run.ID)
			if tc != "shared" {
				if err = e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", work.Login, false); err != nil {
					t.Fatal(err)
				}
			}
			switch tc {
			case "shared":
				if err = e.sched.WakeRun(t.Context(), run.ID, e.member.ID); err != nil || containerState(c) != "running" {
					t.Fatalf("wake = %v, container %s; want it running again", err, containerState(c))
				}
				return
			case "wake after turning off":
				if err = e.sched.WakeRun(t.Context(), run.ID, e.member.ID); err == nil || !strings.Contains(err.Error(), "no longer shared with it") {
					t.Fatalf("wake = %v; want it refused", err)
				}
			case "sweep":
				e.sched.RevokeLogin(work)
			}
			waitFor(t, "the session stopped by the revoke", func() bool {
				row, rowErr := e.db.GetRun(t.Context(), run.ID)
				return rowErr == nil && row.Status.Terminal() && strings.HasPrefix(row.Reason, revokedReason)
			})
			if state := containerState(c); state == "running" {
				t.Fatalf("container %s; want it never started on the login again", state)
			}
		})
	}
}
