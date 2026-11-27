package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// loginUsage reports each login's used share of its Claude five-hour window,
// which resets at resets, or at no time given for a login in noReset; a
// login it has no figure for reads 0%. It counts the fresh reads of each
// login.
type loginUsage struct {
	mu      sync.Mutex
	used    map[domain.Account]float64
	fresh   map[domain.Account]int
	resets  time.Time
	noReset map[domain.Account]bool
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
	resets := &u.resets
	if u.noReset[account] {
		resets = nil
	}
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u.used[account], ResetsAt: resets},
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
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		return rowErr == nil && row.Account() == second
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
	// Beside added logins, a default login that holds no sign-in is passed over.
	writeHomeFile(t, e.adaHome, ".claude/.credentials.json", workLogin)
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
// other login but to a teammate's account, saying the account could not be
// used rather than that its usage ran out, or stops as it would with one
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
				const because = "Ada's account could not be used for this session"
				waitFor(t, "the room note", func() bool {
					return slices.Equal(notes.all(), []string{"Continued on Grace's account because " + because + "."})
				})
				row, rowErr := e.db.GetRun(t.Context(), run.ID)
				c := e.rt.byName(string(run.ID))
				if rowErr != nil || row.Account() != domain.AccountOf(e.owner.ID) || c == nil ||
					!slices.Contains(c.spec.Command, "Your previous turn stopped because "+because+". The session continues on Grace's account. Continue the task where you stopped.") {
					t.Fatalf("run %+v, %v; want it on Grace's account, resumed with the restriction's continuation", row, rowErr)
				}
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

// readHook runs during once, on the first usage read.
type readHook struct {
	fallback.QuotaReader
	once   sync.Once
	during func()
}

func (u *readHook) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	u.once.Do(u.during)
	return u.QuotaReader.Read(ctx, account, refresh)
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
				return rowErr == nil && row.Status.Terminal() && strings.HasPrefix(row.Reason, loginRevokedReason+"Grace turned use for shares off for login work; ")
			})
			if state := containerState(c); state == "running" {
				t.Fatalf("container %s; want it never started on the login again", state)
			}
		})
	}
}

// A move's replacement container on an added login is set up as a launch
// on it is: the variables that would sign Claude Code in are set empty, the
// legacy configuration is masked, and the configuration copy's volume goes
// when the session does. The move is from one added login to another, so
// the replacement's plan writes over the first container's copy and mask.
func TestAMoveToAnAddedLoginIsSetUpAsALaunchOnIt(t *testing.T) {
	t.Parallel()
	e, _, _, usage, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	third := e.addLogin(t, e.member.ID, "lgn-third00000", "third", workLogin)
	usage.set(domain.AccountOf(e.member.ID), 100)
	startScheduler(t, e.sched)
	<-e.sched.RecoveryReady()
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI, domain.LaunchOptions{AccountLogin: second.Login})
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
	e.report(t, run.ID, standardLimit(t))
	waitFor(t, "the session on third", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		if err == nil && row.Status.Terminal() {
			t.Fatalf("run %s (%q); want it moved to third", row.Status, row.Reason)
		}
		c := e.rt.byName(string(run.ID))
		return err == nil && row.Account() == third && c != nil && e.mountsLogin(t, c.spec, third)
	})
	spec := e.rt.byName(string(run.ID)).spec
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if v, ok := spec.Env[key]; !ok || v != "" {
			t.Errorf("the replacement's %s = %q, set %v; want set empty", key, v, ok)
		}
	}
	if !slices.ContainsFunc(spec.Mounts, func(m runtime.Mount) bool {
		return m.ContainerPath == path.Join(spec.Env["HOME"], ".claude/.config.json") && m.ReadOnly
	}) {
		t.Errorf("the replacement has no read-only mask over ~/.claude/.config.json; mounts %+v", spec.Mounts)
	}
	dir := filepath.Join(e.sched.accountConfigRoot(), string(run.ID))
	if err := e.sched.Kill(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the configuration copy and its volume removed", func() bool {
		e.rt.mu.Lock()
		removed := slices.Contains(e.rt.removedVolumes, dir)
		e.rt.mu.Unlock()
		_, statErr := os.Lstat(dir)
		return removed && errors.Is(statErr, fs.ErrNotExist)
	})
}

// Ada hands her own session to Grace, who holds no share of Ada's account.
// It keeps running on Ada's login, default or added, while that login is on
// for shares; when Ada turns it off for shares the session leaves it, and
// the reason says what Ada did.
func TestAHandedOffSessionFollowsItsLoginsShareSwitch(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"default", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, _, notes, _, second := newLoginMoveEnv(t, enhancedClaude)
			login := domain.AccountOf(e.member.ID)
			if name == "second" {
				login = second
			}
			startScheduler(t, e.sched)
			<-e.sched.RecoveryReady()
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: login.Login})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			if err = e.db.TransferRun(t.Context(), run.ID, e.owner.ID); err != nil {
				t.Fatal(err)
			}
			row, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revoked, revokeErr := e.sched.accountRevoked(t.Context(), row); revokeErr != nil || revoked {
				t.Fatalf("accountRevoked = %v, %v; want the handed-off session kept on a shared login", revoked, revokeErr)
			}
			if err = e.db.SetAgentLoginShared(t.Context(), e.member.ID, "claude", cmp.Or(login.Login, domain.DefaultLogin), false); err != nil {
				t.Fatal(err)
			}
			e.sched.RevokeLogin(login)
			want := loginRevokedReason + "Ada turned use for shares off for login " + name + "; no other account can run claude"
			got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
			if got.Reason != want || len(notes.all()) != 0 {
				t.Fatalf("run %q, notes %v; want %q", got.Reason, notes.all(), want)
			}
		})
	}
}

// Grace turns her work login off for shares while Ada's session runs on
// it: the session moves on, and its continuation and room note say what
// Grace did, not that she revoked access to her account.
func TestALoginTurnedOffForSharesSaysSoWhenTheSessionMoves(t *testing.T) {
	t.Parallel()
	e, rt, notes, _, _ := newLoginMoveEnv(t, enhancedClaude)
	e.share(t)
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	startScheduler(t, e.sched)
	<-e.sched.RecoveryReady()
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: work.Login})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if err = e.db.SetAgentLoginShared(t.Context(), e.owner.ID, "claude", work.Login, false); err != nil {
		t.Fatal(err)
	}
	e.sched.RevokeLogin(work)
	const because = "because Grace turned use for shares off for login work."
	waitFor(t, "the room note", func() bool {
		all := notes.all()
		return len(all) == 1 && strings.HasPrefix(all[0], "Continued on ") && strings.HasSuffix(all[0], because)
	})
	waitFor(t, "the continuation prompt", func() bool {
		execs := rt.all()
		sent := sentPrompts(t, execs[len(execs)-1])
		return len(sent) == 1 && strings.HasPrefix(sent[0], "Your previous turn stopped "+because[:len(because)-1]+". ")
	})
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Account() == work || row.Status.Terminal() {
		t.Fatalf("run = %+v, %v; want it live off the work login", row, err)
	}
}

// A workspace variable that signs Claude Code in would pay instead of an
// added login, so a move never chooses one: Ada's session on her default
// login stays in its container rather than lose it to a move that cannot
// start.
func TestAMoveSkipsAddedLoginsWhereAVariablePays(t *testing.T) {
	t.Parallel()
	e, _, notes, _, _ := newLoginMoveEnv(t, enhancedClaude)
	if err := e.db.SetWorkspaceEnvironment(t.Context(), e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}}); err != nil {
		t.Fatal(err)
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := e.rt.byName(string(run.ID))
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	want := "Usage limit reached on Ada's account; no account that may have usage left can run claude here"
	if got.Reason != want || got.Account() != domain.AccountOf(e.member.ID) || e.rt.byName(string(run.ID)) != first || len(notes.all()) != 0 {
		t.Fatalf("run on %s with %q, container kept %v, notes %v; want it kept where it was with %q", got.Account(), got.Reason, e.rt.byName(string(run.ID)) == first, notes.all(), want)
	}
}

// unreadableLogin is loginUsage with one login whose usage cannot be read,
// as a Console login's cannot.
type unreadableLogin struct {
	*loginUsage
	login domain.Account
}

func (u unreadableLogin) Read(ctx context.Context, member domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(member), refresh)
}

func (u unreadableLogin) ReadLogin(ctx context.Context, account domain.Account, refresh bool) ([]quota.Provider, error) {
	if account == u.login {
		return nil, errors.New("usage cannot be read")
	}
	return u.loginUsage.ReadLogin(ctx, account, refresh)
}

// Ada's own session with fallback off runs out on her Console login: it
// stays as --no-fallback asks, and her next launch still passes that login
// over rather than choose it again.
func TestARunOutIsNotedWhenTheSessionCannotMove(t *testing.T) {
	t.Parallel()
	e, _, notes, usage, _ := newLoginMoveEnv(t, enhancedClaude)
	console := e.addLogin(t, e.member.ID, "lgn-console000", "console", "")
	reader := unreadableLogin{loginUsage: usage, login: console}
	e.sched.UseUsageFallback(UsageFallback{Usage: reader, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	t.Cleanup(func() { fallback.ForgetRunOuts(e.member.ID, "claude") })
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP,
		domain.LaunchOptions{AccountLogin: console.Login, Fallback: domain.AccountFallback{Disabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if want := "Usage limit reached on Ada (console)'s account; fallback is off for this session"; got.Reason != want {
		t.Fatalf("reason %q; want %q", got.Reason, want)
	}
	measured, err := fallback.Measure(t.Context(), e.db, reader, e.member.ID, console, "claude", domain.AccountFallback{})
	if err != nil || measured[console] != 100 {
		t.Fatalf("measured %v, %v; want the console login passed over", measured, err)
	}
}

// hangingFreshReads is loginUsage whose fresh reads never answer.
type hangingFreshReads struct{ *loginUsage }

func (u hangingFreshReads) Read(ctx context.Context, member domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(member), refresh)
}

func (u hangingFreshReads) ReadLogin(ctx context.Context, account domain.Account, refresh bool) ([]quota.Provider, error) {
	if refresh {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return u.loginUsage.ReadLogin(ctx, account, refresh)
}

// The fresh read of Grace's login that a teammate's signal needs waits no
// longer than a launch's read: one that does not answer parks the session
// within seconds, not after the move's whole timeout.
func TestAMovesUsageReadIsBounded(t *testing.T) {
	t.Parallel()
	e, _, notes, usage, _ := newLoginMoveEnv(t, enhancedClaude)
	e.share(t)
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	e.sched.UseUsageFallback(UsageFallback{Usage: hangingFreshReads{usage}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: work.Login})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	start := time.Now()
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !strings.HasSuffix(got.Reason, "Grace (work)'s usage does not show") || time.Since(start) > 20*time.Second {
		t.Fatalf("run %q after %s; want it parked on the unanswered read within seconds", got.Reason, time.Since(start))
	}
}

// A session that left Ada's account on a seat restriction does not come
// back to her second login when Grace's runs out later in the session: the
// restriction holds for every login of Ada's account.
func TestARestrictedAccountIsNotReturnedTo(t *testing.T) {
	t.Parallel()
	e, _, _, usage, second := newLoginMoveEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	e.report(t, run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"})
	seat, ok := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"Your seat type doesn't include usage credits"}`))
	if !ok {
		t.Fatal("the StopFailure hook was not mapped")
	}
	e.report(t, run.ID, seat)
	waitFor(t, "the session on Grace's account", func() bool {
		row, rowErr := e.db.GetRun(t.Context(), run.ID)
		return rowErr == nil && row.Account() == domain.AccountOf(e.owner.ID) && e.rt.byName(string(run.ID)) != nil
	})
	usage.set(domain.AccountOf(e.owner.ID), 100)
	waitFor(t, "the replacement's report", func() bool {
		return e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}) == nil
	})
	e.report(t, run.ID, standardLimit(t))
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got.Account() != domain.AccountOf(e.owner.ID) || usage.freshReads(second) != 0 {
		t.Fatalf("run on %s (%q); want it kept on Grace's account, Ada's second login not tried", got.Account(), got.Reason)
	}
}

// Ada keeps her default login as an API key beside a subscription login.
// A session that runs out on the subscription and moves to the default
// login says it is billed per use, as a move to an added Console login
// does: in its log, its room note, its run.account event and its row.
func TestAMoveToADefaultLoginBilledPerUseIsSaidSo(t *testing.T) {
	t.Parallel()
	e, _, notes, _, second := newLoginMoveEnv(t, enhancedClaude)
	writeHomeFile(t, e.adaHome, ".claude.json", `{"primaryApiKey":"test-not-a-key-of-ada","numStartups":2}`)
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunAccount}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP, domain.LaunchOptions{AccountLogin: second.Login})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	const billed = "Ada's account is billed per use."
	select {
	case ev := <-sub.Events():
		if p, ok := ev.Payload.(events.RunAccountPayload); !ok || !p.PerUse || p.Login != "" {
			t.Fatalf("run.account = %+v; want the default login, billed per use", ev.Payload)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no run.account event")
	}
	waitFor(t, "the room note", func() bool {
		return slices.Equal(notes.all(), []string{"Continued on Ada's account because Ada (second)'s usage ran out. " + billed})
	})
	waitFor(t, "the log line", func() bool {
		log, readErr := os.ReadFile(e.pty.ItemLogPath(run.ID))
		return readErr == nil && strings.Contains(string(log), billed)
	})
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || !row.AccountLoginPerUse || row.Account() != domain.AccountOf(e.member.ID) {
		t.Fatalf("run = %+v, %v; want it on the default login, billed per use", row, err)
	}
}

// Ada's session runs out on her default login and then on Grace's, with
// Ada's second login spent too. The reason lists the two logins the session
// ran on, across both accounts, and when the first is usable again: unknown
// once a spent window of one of them gives no reset time.
func TestTheAllSpentReasonNamesTheLoginsTheSessionRanOn(t *testing.T) {
	t.Parallel()
	for _, reset := range []bool{true, false} {
		t.Run(map[bool]string{true: "reset known", false: "reset unknown"}[reset], func(t *testing.T) {
			t.Parallel()
			e, _, _, usage, second := newLoginMoveEnv(t, enhancedClaude)
			e.share(t)
			grace := domain.AccountOf(e.owner.ID)
			usage.set(second, 100)
			if !reset {
				usage.noReset = map[domain.Account]bool{grace: true}
			}
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			waitFor(t, "the session on Grace's account", func() bool {
				row, rowErr := e.db.GetRun(t.Context(), run.ID)
				sc, scErr := e.sched.readSidecar(run.ID)
				_, liveErr := e.sched.acp.live(t.Context(), run.ID)
				return rowErr == nil && scErr == nil && liveErr == nil && row.Account() == grace && sc.AccountSwap == nil
			})
			usage.set(domain.AccountOf(e.member.ID), 100)
			usage.set(grace, 100)
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
			usable := usage.resets.UTC().Format(time.RFC3339)
			if !reset {
				usable = "unknown"
			}
			if want := "Usage limit reached on Grace's account; no fallback has usage left; logins tried: Ada, Grace; the first is usable again at " + usable; got.Reason != want {
				t.Fatalf("reason %q; want %q", got.Reason, want)
			}
		})
	}
}
