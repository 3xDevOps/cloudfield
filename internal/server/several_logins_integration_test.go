//go:build integration

package server

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// runOutCue is the file a test writes into a session's checkout to make
// cuedClaude report its usage limit.
const runOutCue = ".run-out"

// cuedClaude stands in for claude -p --output-format stream-json. Each start
// records its container spec, opens its turn with an init line and waits
// for runOutCue in its checkout; on the cue it removes the file, prints
// Claude Code's usage-limit stream lines and exits 1, as Claude Code does.
type cuedClaude struct {
	limit  string
	mu     sync.Mutex
	starts []runtime.Spec
}

func newCuedClaude(t *testing.T) *cuedClaude {
	t.Helper()
	limit, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", "claude_usage_limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &cuedClaude{limit: string(limit)}
}

func (a *cuedClaude) run(c *e2eContainer) {
	a.mu.Lock()
	a.starts = append(a.starts, c.spec)
	a.mu.Unlock()
	c.output(`{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s1","tools":[],"mcp_servers":[],"model":"claude-sonnet-4-5","permissionMode":"bypassPermissions"}` + "\r\n")
	cue := filepath.Join(c.spec.WorktreeHostPath, runOutCue)
	for os.Remove(cue) != nil {
		select {
		case <-c.done:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	c.output(strings.ReplaceAll(a.limit, "\n", "\r\n"))
	time.Sleep(500 * time.Millisecond)
	c.exitNow(1)
}

// started waits for the nth start and returns every start so far.
func (a *cuedClaude) started(t *testing.T, n int) []runtime.Spec {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		a.mu.Lock()
		starts := slices.Clone(a.starts)
		a.mu.Unlock()
		if len(starts) >= n {
			return starts
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in agent started %d times; want %d", len(starts), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cue tells the latest start to report its usage limit.
func (a *cuedClaude) cue(t *testing.T) {
	t.Helper()
	a.mu.Lock()
	checkout := a.starts[len(a.starts)-1].WorktreeHostPath
	a.mu.Unlock()
	writeFile(t, filepath.Join(checkout, runOutCue), "")
}

// countedRuntime counts the containers created for each run, which the
// e2e runtime forgets once they are destroyed.
type countedRuntime struct {
	*loginsRuntime
	createMu sync.Mutex
	created  map[string]int
}

func newCountedRuntime() *countedRuntime {
	return &countedRuntime{loginsRuntime: &loginsRuntime{e2eRuntime: newE2ERuntime()}, created: map[string]int{}}
}

func (r *countedRuntime) Create(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	id, err := r.loginsRuntime.Create(ctx, spec)
	if err == nil {
		r.createMu.Lock()
		r.created[spec.CreationKey]++
		r.createMu.Unlock()
	}
	return id, err
}

// runStarts counts the containers created for run, whatever they run.
func (r *countedRuntime) runStarts(run string) int {
	r.createMu.Lock()
	defer r.createMu.Unlock()
	return r.created[run]
}

// resetLoginUsage is stubLoginUsage whose default login and added logins
// reset at times of their own.
type resetLoginUsage struct {
	*stubLoginUsage
	defaultReset, addedReset time.Time
}

func (u *resetLoginUsage) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(account), refresh)
}

func (u *resetLoginUsage) ReadLogin(ctx context.Context, account domain.Account, refresh bool) ([]quota.Provider, error) {
	providers, err := u.stubLoginUsage.ReadLogin(ctx, account, refresh)
	reset := u.defaultReset
	if account.Login.Added() {
		reset = u.addedReset
	}
	providers[0].Windows[0].ResetsAt = &reset
	return providers, err
}

// sessionRow is the line cloudfield sessions prints for id.
func sessionRow(t *testing.T, table, id string) string {
	t.Helper()
	for _, row := range strings.Split(table, "\n") {
		if fields := strings.Fields(row); len(fields) > 0 && fields[0] == id {
			return row
		}
	}
	t.Fatalf("cloudfield sessions does not list %s:\n%s", id, table)
	return ""
}

type sessionSnapshot struct {
	State            string `json:"state"`
	Reason           string `json:"reason"`
	AccountLogin     string `json:"account_login"`
	AccountLoginName string `json:"account_login_name"`
}

// TestIntegrationSeveralLoginsEndToEnd drives several Claude Code logins
// through the built CLI and a real in-process server, with a stand-in
// agent that runs out on cue: add a login, launch, run out, move to the
// added login, run out there too.
func TestIntegrationSeveralLoginsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	rt := newCountedRuntime()
	now := time.Now().Truncate(time.Second)
	usage := &resetLoginUsage{stubLoginUsage: &stubLoginUsage{used: map[domain.Account]float64{}}, defaultReset: now.Add(2 * time.Hour), addedReset: now.Add(time.Hour)}
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	sub := srv.subscribe(ctx, t)
	var (
		eventsMu  sync.Mutex
		published []string
	)
	go func() {
		for ev := range sub.Events() {
			b, _ := json.Marshal(ev)
			eventsMu.Lock()
			published = append(published, string(b))
			eventsMu.Unlock()
		}
	}()
	signInDefault(t, e.dataDir, e.ada.id)
	installed := filepath.Join(e.dataDir, "homes", string(e.ada.id), ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ada := newCLIUser(t, buildCLIBinary(t), srv.addr, e.keyPath)
	var said strings.Builder
	cli := func(args ...string) string {
		t.Helper()
		out, stderr, code := ada.cli(args...)
		said.WriteString(out + stderr)
		t.Logf("$ cloudfield %s\n%s%s", strings.Join(args, " "), out, stderr)
		if code != 0 {
			t.Fatalf("cloudfield %v exited %d: %s", args, code, stderr)
		}
		return out
	}
	snapshot := func(id string) sessionSnapshot {
		t.Helper()
		var s sessionSnapshot
		if err := json.Unmarshal([]byte(cli("sessions", id, "--json")), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 1. Add: a second login, signed in on a terminal through the stand-in
	// for claude auth login.
	rt.mu.Lock()
	rt.signIn = func(credentials, config string) {
		writeFile(t, credentials, `{"claudeAiOauth":{"accessToken":"test-access-second","refreshToken":"test-refresh-second","expiresAt":4102444800000,"subscriptionType":"max"}}`)
		writeFile(t, config, `{"oauthAccount":{"emailAddress":"ada@second.example.test","organizationUuid":"org-second","organizationName":"Second Org"}}`)
	}
	rt.mu.Unlock()
	shown, code := ada.onTerminal(map[string]string{"Paste code here": "test-code\r"}, "agent", "login", "claude", "--as", "second")
	said.WriteString(shown)
	t.Logf("$ cloudfield agent login claude --as second\n%s", shown)
	if code != 0 || !strings.Contains(shown, "added login second: subscription, plan max, ada@second.example.test, organisation Second Org\n"+
		"Automatic is on: Cloudfield may start or move your sessions onto it; turn it off with: cloudfield agent logins auto second off\n"+
		"Use for shares is on: teammates you share your account with can be served by it; turn it off with: cloudfield agent logins share second off\n") {
		t.Fatalf("agent login claude --as second = %d:\n%s", code, shown)
	}
	rows, err := srv.srv.Store().ListAgentLogins(ctx, e.ada.id, "claude")
	if err != nil || len(rows) != 2 || rows[0].ID != domain.DefaultLogin || rows[1].Name != "second" {
		t.Fatalf("agent_logins after the add = %+v, %v; want default then second", rows, err)
	}
	second := domain.Account{Member: e.ada.id, Login: rows[1].ID}
	usage.set(domain.AccountOf(e.ada.id), 30)
	usage.set(second, 20)
	if out := cli("agent", "logins"); out != "#  LOGIN    KIND          PLAN  EMAIL                    ORGANISATION  USAGE  AUTOMATIC  SHARES  BILLED  YOUR SESSIONS\n"+
		"1  default  subscription  -     -                        -              30%   on         on      -       0\n"+
		"2  second   subscription  max   ada@second.example.test  Second Org     20%   on         on      -       0\n" {
		t.Fatalf("agent logins after the add:\n%s", out)
	}

	// 2. Launch: both logins have usage left, so the session starts on the
	// first in the order, the default login, though it has used more.
	const task = "work until a login runs out"
	agent := newCuedClaude(t)
	rt.script(task, agent.run)
	const continuation = "Your previous turn stopped because Ada's usage ran out. The session continues on Ada (second)'s account. Continue the task where you stopped."
	rt.script(continuation, agent.run)
	out := cli("session", task, "--agent", "claude", "--mode", "background")
	if !strings.HasPrefix(out, "session run-") {
		t.Fatalf("session printed %q; want the session id", out)
	}
	id := strings.Fields(out)[1]
	ctrl, client := srv.control(t, e.ada.key)
	defer func() { _ = client.Close() }()
	waitLoginRun(t, ctrl, id, func(r protocol.Run) bool { return r.Status == string(domain.RunRunning) })
	starts := agent.started(t, 1)
	if row := sessionRow(t, cli("sessions"), id); len(strings.Fields(row)) < 5 || strings.Fields(row)[4] != "Ada" || strings.Contains(row, "Ada, login") {
		t.Fatalf("the launched session lists %q; want Ada's default login", row)
	}
	if s := snapshot(id); s.State != string(domain.RunRunning) || s.AccountLogin != "" {
		t.Fatalf("the launched session is %+v; want running on the default login", s)
	}
	loginsDir := filepath.Join(e.dataDir, "logins")
	if !slices.ContainsFunc(starts[0].Command, func(arg string) bool { return arg == task || strings.HasPrefix(arg, task+"\n") }) ||
		slices.ContainsFunc(starts[0].Mounts, func(m runtime.Mount) bool { return strings.HasPrefix(m.HostPath, loginsDir) }) {
		t.Fatalf("the first start: argv %q, mounts %+v; want the task, and no added login mounted", starts[0].Command, starts[0].Mounts)
	}

	// 3. Run out on cue: the default login is spent, and the stand-in says so.
	usage.set(domain.AccountOf(e.ada.id), 100)
	agent.cue(t)

	// 4. Move: a new start on login second resumes the conversation with one
	// continuation that names both logins, and never sends the task again.
	waitLoginRun(t, ctrl, id, func(r protocol.Run) bool { return r.AccountLogin == string(second.Login) })
	starts = agent.started(t, 2)
	argv := starts[1].Command
	sent := 0
	for _, arg := range argv {
		if arg == continuation || strings.HasPrefix(arg, continuation+"\n") {
			sent++
		}
	}
	at := slices.Index(argv, "--resume")
	if sent != 1 || at < 0 || at+1 >= len(argv) || argv[at+1] != "s1" || slices.ContainsFunc(argv, func(arg string) bool { return strings.Contains(arg, task) }) {
		t.Fatalf("the start after the move: argv %q; want --resume s1 and the continuation once, without the task", argv)
	}
	secondDir := filepath.Join(loginsDir, string(e.ada.id), "claude", string(second.Login))
	home := starts[1].Env["HOME"]
	if home == "" || !slices.ContainsFunc(starts[1].Mounts, func(m runtime.Mount) bool {
		return m.HostPath == secondDir && m.Subpath == memberhome.LoginCredentials && m.ContainerPath == path.Join(home, ".claude", ".credentials.json")
	}) {
		t.Fatalf("the start after the move does not mount login second's credentials at $HOME/.claude/.credentials.json; HOME %q, mounts %+v", home, starts[1].Mounts)
	}
	if profile, set := starts[1].Env["ANTHROPIC_PROFILE"]; starts[1].Env["CLAUDE_CONFIG_DIR"] != path.Join(home, ".claude") ||
		starts[1].Env["ANTHROPIC_CONFIG_DIR"] != path.Join(home, ".config", "anthropic") || !set || profile != "" {
		t.Fatalf("the start after the move: env %q; want CLAUDE_CONFIG_DIR=$HOME/.claude, ANTHROPIC_CONFIG_DIR=$HOME/.config/anthropic and ANTHROPIC_PROFILE empty", starts[1].Env)
	}
	if row := sessionRow(t, cli("sessions"), id); !strings.Contains(row, " Ada, login second ") {
		t.Fatalf("the moved session lists %q; want login second", row)
	}
	if s := snapshot(id); s.State != string(domain.RunRunning) || s.AccountLogin != string(second.Login) || s.AccountLoginName != "second" {
		t.Fatalf("the moved session is %+v; want running on login second", s)
	}
	segments, err := srv.srv.Store().ListRunAccountSegments(ctx, domain.RunID(id))
	if err != nil || len(segments) != 2 || segments[0].Account() != domain.AccountOf(e.ada.id) || segments[1].Account() != second {
		t.Fatalf("account segments = %+v, %v; want the default login then second", segments, err)
	}
	const note = "Continued on Ada (second)'s account because Ada's usage ran out."
	room := func() []string {
		t.Helper()
		page, err := srv.srv.Store().ListRoomMessages(ctx, e.ws.ID, domain.RunID(id), "", 50)
		if err != nil {
			t.Fatal(err)
		}
		var bodies []string
		for _, m := range page.Items {
			if m.Kind == store.RoomMessageSystem {
				bodies = append(bodies, m.Body)
			}
		}
		return bodies
	}
	for deadline := time.Now().Add(time.Minute); !slices.Contains(room(), note); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the room's notes %q do not include %q", room(), note)
		}
	}
	moved := events.RunAccountPayload{Account: e.ada.id, Previous: e.ada.id, Login: second.Login, LoginName: "second"}
	movedEvent := func() bool {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		return slices.ContainsFunc(published, func(ev string) bool {
			var event struct {
				Type    events.Type
				RunID   domain.RunID
				Payload json.RawMessage
			}
			var payload events.RunAccountPayload
			return json.Unmarshal([]byte(ev), &event) == nil && event.Type == events.TypeRunAccount && event.RunID == domain.RunID(id) &&
				json.Unmarshal(event.Payload, &payload) == nil && payload == moved
		})
	}
	for deadline := time.Now().Add(time.Minute); !movedEvent(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no run.account event names the move from the default login to second")
		}
	}

	// 5. All spent: login second runs out too, and no login or account is
	// left, so the session ends with the logins it tried.
	usage.set(second, 100)
	agent.cue(t)
	ended := waitLoginRun(t, ctrl, id, func(r protocol.Run) bool { return r.Status == string(domain.RunFailed) })
	want := "Usage limit reached on Ada (second)'s account; no fallback has usage left; logins tried: Ada, Ada (second); the first is usable again at " + usage.addedReset.UTC().Format(time.RFC3339)
	if ended.Reason != want || ended.AccountLogin != string(second.Login) {
		t.Fatalf("the session ended on login %q with %q; want login second and %q, the earliest of the logins' resets", ended.AccountLogin, ended.Reason, want)
	}
	if s := snapshot(id); s.State != string(domain.RunFailed) || s.Reason != ended.Reason {
		t.Fatalf("cloudfield sessions %s --json = %+v; want failed with %q", id, s, ended.Reason)
	}
	cli("logs", id)
	notes := room()
	if moves := slices.DeleteFunc(slices.Clone(notes), func(n string) bool { return !strings.HasPrefix(n, "Continued on") }); !slices.Equal(moves, []string{note}) {
		t.Fatalf("the room's notes %q; want the one move", notes)
	}
	settled := func() {
		t.Helper()
		if n, starts := rt.runStarts(id), agent.started(t, 2); n != 2 || len(starts) != 2 {
			t.Fatalf("the runtime created %d containers for the session and the stand-in started %d times; want 2, nothing replayed after the last limit", n, len(starts))
		}
	}
	settled()

	time.Sleep(2 * time.Second)
	settled()

	eventsMu.Lock()
	collected := said.String() + strings.Join(published, "\n") + strings.Join(notes, "\n") + ended.Reason
	eventsMu.Unlock()
	for _, secret := range []string{"test-access-default", "test-refresh-default", "test-access-second", "test-refresh-second"} {
		if strings.Contains(collected, secret) {
			t.Errorf("%q from a login's files appears in the CLI output, events, room notes or reason", secret)
		}
	}
}

// TestIntegrationSeveralLoginsOneLoginControl: a member who never added a
// login launches and runs out as before logins existed: no row, no login
// directory, one start, and the reason of a session with nowhere to go.
func TestIntegrationSeveralLoginsOneLoginControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rt := newCountedRuntime()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	signInDefault(t, e.dataDir, e.ada.id)
	usage.set(domain.AccountOf(e.ada.id), 10)
	ada := newCLIUser(t, buildCLIBinary(t), srv.addr, e.keyPath)
	const task = "work until the only account runs out"
	agent := newCuedClaude(t)
	rt.script(task, agent.run)

	out, stderr, code := ada.cli("session", task, "--agent", "claude", "--mode", "background")
	id, _, _ := strings.Cut(strings.TrimPrefix(out, "session "), " ")
	if code != 0 || !strings.HasPrefix(id, "run-") || out != "session "+id+" running\n" ||
		!regexp.MustCompile(`^aether: trusting new host [^\n]+\n$`).MatchString(stderr) {
		t.Fatalf("session = %d %q %q; want the session id and its status, and only the new host noted", code, out, stderr)
	}
	agent.started(t, 1)
	usage.set(domain.AccountOf(e.ada.id), 100)
	agent.cue(t)
	ctrl, client := srv.control(t, e.ada.key)
	defer func() { _ = client.Close() }()
	ended := waitLoginRun(t, ctrl, id, func(r protocol.Run) bool { return r.Status == string(domain.RunFailed) })
	if ended.Reason != "Usage limit reached on Ada's account; no other account is shared to fall back on" || ended.AccountLogin != "" {
		t.Fatalf("the session ended on login %q with %q; want the reason from before logins", ended.AccountLogin, ended.Reason)
	}
	table, stderr, code := ada.cli("sessions")
	if code != 0 {
		t.Fatalf("sessions = %d %q", code, stderr)
	}
	if row := sessionRow(t, table, id); len(strings.Fields(row)) < 5 || strings.Fields(row)[4] != "Ada" || strings.Contains(row, "Ada, login") {
		t.Fatalf("the session lists %q; want Ada's account as before", row)
	}
	shown, stderr, code := ada.cli("sessions", id)
	if code != 0 || !regexp.MustCompile(`(?m)^state +failed$`).MatchString(shown) || regexp.MustCompile(`(?m)^login\b`).MatchString(shown) {
		t.Fatalf("sessions %s = %d %q:\n%s; want the failed session without a login row", id, code, stderr, shown)
	}
	shown, stderr, code = ada.cli("sessions", id, "--json")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(shown), &fields); code != 0 || err != nil || string(fields["state"]) != `"failed"` {
		t.Fatalf("sessions %s --json = %d %q %v:\n%s", id, code, stderr, err, shown)
	}
	for field := range fields {
		if strings.HasPrefix(field, "account_login") {
			t.Fatalf("sessions %s --json carries %s; want no login field:\n%s", id, field, shown)
		}
	}
	time.Sleep(2 * time.Second)
	if n, starts := rt.runStarts(id), agent.started(t, 1); n != 1 || len(starts) != 1 {
		t.Fatalf("the runtime created %d containers for the session and the stand-in started %d times; want once", n, len(starts))
	}
	if rows, err := srv.srv.Store().ListAgentLogins(ctx, e.ada.id, "claude"); err != nil || len(rows) != 0 {
		t.Fatalf("agent_logins = %+v, %v; want no row", rows, err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "logins", string(e.ada.id))); !os.IsNotExist(err) {
		t.Fatalf("Ada has a logins directory (%v); want none", err)
	}
	page, err := srv.srv.Store().ListRoomMessages(ctx, e.ws.ID, domain.RunID(id), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(page.Items, func(m *store.RoomMessage) bool { return strings.HasPrefix(m.Body, "Continued on") }) {
		t.Fatal("the room notes a move; want none")
	}
}
