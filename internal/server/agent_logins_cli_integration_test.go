//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/envname"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// loginsRuntime runs the sign-in of an added login as a stand-in for
// claude auth login: it asks for a code, then signIn saves the login in the
// files the container mounts at ~/.claude/.credentials.json and
// ~/.claude/.claude.json.
type loginsRuntime struct {
	*e2eRuntime
	mu     sync.Mutex
	codes  []string
	signIn func(credentials, config string)
}

func (r *loginsRuntime) ExecTTY(ctx context.Context, id runtime.ID, argv []string, dir string, cols, rows uint) (runtime.Attachment, error) {
	c, err := r.get(id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(c.spec.Name, "aether-login-") {
		return r.e2eRuntime.ExecTTY(ctx, id, argv, dir, cols, rows)
	}
	if strings.Join(argv, " ") != "claude auth login" {
		return nil, fmt.Errorf("sign-in argv %q, want claude auth login", argv)
	}
	mounted := map[string]string{}
	for _, m := range c.spec.Mounts {
		mounted[strings.TrimPrefix(m.ContainerPath, c.spec.Env["HOME"]+"/")] = filepath.Join(m.HostPath, m.Subpath)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		defer func() { _ = outW.Close() }()
		_, _ = io.WriteString(outW, "Paste code here if prompted > ")
		var code []byte
		b := make([]byte, 1)
		for {
			if _, err := inR.Read(b); err != nil || b[0] == '\r' || b[0] == '\n' {
				break
			}
			code = append(code, b[0])
		}
		r.mu.Lock()
		r.codes = append(r.codes, string(code))
		save := r.signIn
		r.mu.Unlock()
		save(mounted[".claude/.credentials.json"], mounted[".claude/.claude.json"])
		_, _ = io.WriteString(outW, "\r\nLogin successful.\r\n")
	}()
	return &standInTerminal{in: inW, inR: inR, out: outR, outW: outW}, nil
}

type standInTerminal struct {
	in   *io.PipeWriter
	inR  *io.PipeReader
	out  *io.PipeReader
	outW *io.PipeWriter
}

func (a *standInTerminal) Stdin() io.WriteCloser                    { return a.in }
func (a *standInTerminal) Stdout() io.Reader                        { return a.out }
func (a *standInTerminal) Stderr() io.Reader                        { return bytes.NewReader(nil) }
func (a *standInTerminal) Resize(context.Context, uint, uint) error { return nil }
func (a *standInTerminal) Close() error {
	_ = a.inR.Close()
	return a.outW.Close()
}

// loginsUsage reads every login's Claude Code five-hour window as
// used[login], made-up figures that need no vendor.
type loginsUsage struct {
	mu   sync.Mutex
	used map[domain.LoginID]float64
}

func (u *loginsUsage) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.Account{Member: account}, refresh)
}

func (u *loginsUsage) ReadLogin(_ context.Context, account domain.Account, _ bool) ([]quota.Provider, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	login := account.Login
	if login == "" {
		login = domain.DefaultLogin
	}
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{{ID: "five_hour", Label: "5h", UsedPercent: u.used[login]}}}}, nil
}

// cliUser runs the linked CLI as one member, on a pipe or a terminal.
type cliUser struct {
	*sshUser
	aether string
}

// newCLIUser links the CLI to the server at addr from a home of its own.
// Its environment is set whole: no CLOUDFIELD_ or AETHER_ variable of the
// machine running the test reaches it, and both names of the config
// directory variable point at the link written here.
func newCLIUser(t *testing.T, aether, addr, keyPath string) *cliUser {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configDir, "aether"), 0o700); err != nil {
		t.Fatal(err)
	}
	link, err := json.Marshal(cli.Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "aether", "config.json"), string(link))
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SSH_AUTH_SOCK=",
		cli.ConfigDirEnv + "=" + configDir, envname.New(cli.ConfigDirEnv) + "=" + configDir}
	return &cliUser{sshUser: &sshUser{t: t, aether: aether, env: env}, aether: aether}
}

func (u *cliUser) cli(args ...string) (stdout, stderr string, code int) {
	u.t.Helper()
	return u.run("", u.aether, args...)
}

// onTerminal runs the CLI with a terminal for stdin and stdout, answers
// each prompt in answers with its reply once it appears, and returns all
// the terminal showed, without carriage returns.
func (u *cliUser) onTerminal(answers map[string]string, args ...string) (string, int) {
	u.t.Helper()
	return u.terminal(answers, "", args...)
}

// closedAt runs the CLI on a terminal and kills it once it shows prompt,
// as closing the terminal window would, and returns what it showed.
func (u *cliUser) closedAt(prompt string, args ...string) string {
	u.t.Helper()
	shown, _ := u.terminal(nil, prompt, args...)
	return shown
}

func (u *cliUser) terminal(answers map[string]string, closeAt string, args ...string) (string, int) {
	t := u.t
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	if err = unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmd := u.command(u.aether, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	var (
		mu   sync.Mutex
		seen bytes.Buffer
	)
	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 4096)
		answered := map[string]bool{}
		for {
			n, rerr := master.Read(buf)
			mu.Lock()
			seen.Write(buf[:n])
			text := seen.String()
			mu.Unlock()
			for prompt, reply := range answers {
				if !answered[prompt] && strings.Contains(text, prompt) {
					answered[prompt] = true
					_, _ = io.WriteString(master, reply)
				}
			}
			if closeAt != "" && strings.Contains(text, closeAt) {
				_ = cmd.Process.Kill()
			}
			if rerr != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(time.Minute):
		_ = cmd.Process.Kill()
		mu.Lock()
		shown := seen.String()
		mu.Unlock()
		t.Fatalf("aether %v on a terminal did not end; it showed:\n%s", args, shown)
	}
	<-read
	code := 0
	if exit, ok := waitErr.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if waitErr != nil {
		t.Fatal(waitErr)
	}
	return strings.ReplaceAll(seen.String(), "\r", ""), code
}

// TestIntegrationAgentLoginsCLI drives cloudfield agent login --as and
// cloudfield agent logins against a server whose sign-in container is a
// stand-in for claude auth login. A member with only the default login
// gets no row and no change to agent login; one who adds a login gets the
// server's view of it and every refusal word for word.
func TestIntegrationAgentLoginsCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rt := &loginsRuntime{e2eRuntime: newE2ERuntime()}
	usage := &loginsUsage{used: map[domain.LoginID]float64{domain.DefaultLogin: 12}}
	data := filepath.Join(t.TempDir(), "d")
	srv, err := New(ctx, Config{DataDir: data, Addr: "127.0.0.1:0", Runtime: rt, StandardImage: "e2e/fake", Usage: usage})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	ran := make(chan error, 1)
	go func() { ran <- srv.Run(runCtx) }()
	defer func() {
		stop()
		select {
		case err := <-ran:
			if err != nil {
				t.Errorf("server.Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("server did not shut down")
		}
	}()
	addr := waitSSHAddr(t, srv)

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#4363d8", Role: domain.RoleSuperAdmin}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		installed := filepath.Join(data, "homes", string(member.ID), ".local", "bin", agent)
		if err = os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, installed, "#!/bin/sh\n")
		if err = os.Chmod(installed, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	aether := buildCLIBinary(t)
	ada := newCLIUser(t, aether, addr, keyPath)

	rows := func() []domain.AgentLogin {
		t.Helper()
		logins, lerr := srv.Store().ListAgentLogins(ctx, member.ID, "claude")
		if lerr != nil {
			t.Fatal(lerr)
		}
		return logins
	}
	logins := func() string {
		t.Helper()
		out, stderr, code := ada.cli("agent", "logins")
		if code != 0 {
			t.Fatalf("agent logins exited %d: %s", code, stderr)
		}
		t.Logf("$ cloudfield agent logins\n%s", out)
		return out
	}

	// One login: the default, read from the home; no row is written and
	// agent login without --as says what it said before.
	if out := logins(); out != "#  LOGIN    KIND  PLAN  EMAIL  ORGANISATION  USAGE   AUTOMATIC  SHARES  BILLED  YOUR SESSIONS\n"+
		"1  default  -     -     -      -             5h 12%  on         on      -       0\n" {
		t.Fatalf("agent logins with only the default login:\n%s", out)
	}
	out, stderr, code := ada.cli("agent", "logins", "claude", "--json")
	var listed []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Usage struct {
			Windows []struct {
				UsedPercent float64 `json:"used_percent"`
			} `json:"windows"`
		} `json:"usage"`
		Sessions []string `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &listed); code != 0 || err != nil || len(listed) != 1 || listed[0].ID != "default" || listed[0].Name != "default" ||
		len(listed[0].Usage.Windows) != 1 || listed[0].Usage.Windows[0].UsedPercent != 12 || listed[0].Sessions == nil || len(listed[0].Sessions) != 0 {
		t.Fatalf("agent logins claude --json = %d %q %q (%v); want one document: the default login, 12%% used, no sessions", code, out, stderr, err)
	}
	_, stderr, code = ada.cli("agent", "login", "claude")
	if code == 0 || !strings.Contains(stderr, "claude signs in with claude auth login, which needs typed input, so it needs a terminal. The human runs this in their own terminal:\n  aether agent login claude\n") {
		t.Fatalf("agent login claude without a terminal = %d %q; want today's instruction", code, stderr)
	}
	_, stderr, code = ada.cli("agent", "login", "claude", "--as", "work")
	if code == 0 || !strings.Contains(stderr, "The human runs this in their own terminal:\n  cloudfield agent login claude --as work") {
		t.Fatalf("agent login claude --as work without a terminal = %d %q", code, stderr)
	}
	t.Logf("$ cloudfield agent login claude --as work </dev/null\n%s", stderr)
	_, stderr, code = ada.cli("agent", "login", "codex", "--as", "work")
	t.Logf("$ cloudfield agent login codex --as work </dev/null\n%s", stderr)
	if code == 0 || !strings.Contains(stderr, "only Claude Code keeps added logins, so codex has none; cloudfield agent login codex signs in its one login\n") {
		t.Fatalf("agent login codex --as work without a terminal = %d %q", code, stderr)
	}
	shown, code := ada.onTerminal(nil, "agent", "login", "claude", "--as=")
	t.Logf("$ cloudfield agent login claude --as=\n%s", shown)
	if code == 0 || shown != "aether: --as needs a name: cloudfield agent login claude --as <name>\n" {
		t.Fatalf("agent login claude --as= on a terminal = %d:\n%s", code, shown)
	}
	shown, code = ada.onTerminal(nil, "agent", "login", "codex", "--attach", "lgn-test1")
	t.Logf("$ cloudfield agent login codex --attach lgn-test1\n%s", shown)
	if code == 0 || shown != "aether: only Claude Code keeps added logins, so codex has none; cloudfield agent login codex signs in its one login\n" {
		t.Fatalf("agent login codex --attach on a terminal = %d:\n%s", code, shown)
	}
	shown, code = ada.onTerminal(nil, "agent", "login", "claude", "--attach", "lgn-test1")
	t.Logf("$ cloudfield agent login claude --attach lgn-test1\n%s", shown)
	if code != 1 || shown != "returning to the sign-in of login lgn-test1: claude auth login runs in a container of its own; follow its prompts\n"+
		"aether: cli: terminal: store: not found: no sign-in of a login lgn-test1\n" {
		t.Fatalf("agent login claude --attach with an unknown id = %d:\n%s", code, shown)
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("agent_logins rows before any login was added: %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "logins")); len(entries) != 0 {
		t.Fatalf("login directories before any login was added: %v", entries)
	}

	if _, stderr, code := ada.cli("agent", "logins", "share", "default", "off"); code == 0 || !strings.Contains(stderr, "you have only the default login; add a login first") {
		t.Fatalf("agent logins share default off with only the default login = %d %q", code, stderr)
	}

	// Add a login on a terminal: the code typed reaches the sign-in, and
	// the CLI prints the login the server added.
	saveAs := func(email, org string) func(string, string) {
		return func(creds, config string) {
			writeFile(t, creds, `{"claudeAiOauth":{"accessToken":"test-access-work","refreshToken":"test-refresh-work","expiresAt":4102444800000,"subscriptionType":"max"}}`)
			writeFile(t, config, `{"oauthAccount":{"emailAddress":"`+email+`","organizationUuid":"`+org+`","organizationName":"Test Org"}}`)
		}
	}
	rt.mu.Lock()
	rt.signIn = saveAs("ada@work.example.test", "org-work")
	rt.mu.Unlock()
	shown, code = ada.onTerminal(map[string]string{"Paste code here": "test-code\r"}, "agent", "login", "claude", "--as", "work")
	t.Logf("$ cloudfield agent login claude --as work\n%s", shown)
	if code != 0 || !strings.Contains(shown, "added login work: subscription, plan max, ada@work.example.test, organisation Test Org\n"+
		"Automatic is on: Cloudfield may start or move your sessions onto it; turn it off with: cloudfield agent logins auto work off\n"+
		"Use for shares is on: teammates you share your account with can be served by it; turn it off with: cloudfield agent logins share work off\n") {
		t.Fatalf("agent login claude --as work on a terminal = %d:\n%s", code, shown)
	}
	rt.mu.Lock()
	if len(rt.codes) != 1 || rt.codes[0] != "test-code" {
		t.Errorf("the sign-in read %q; want the code typed", rt.codes)
	}
	rt.mu.Unlock()
	work := rows()
	if len(work) != 2 || work[1].Name != "work" {
		t.Fatalf("rows after adding work: %+v", work)
	}
	usage.mu.Lock()
	usage.used[work[1].ID] = 40
	usage.mu.Unlock()

	// The same account again, and a reserved name: the server's refusals.
	shown, code = ada.onTerminal(map[string]string{"Paste code here": "test-code\r"}, "agent", "login", "claude", "--as", "again")
	t.Logf("$ cloudfield agent login claude --as again\n%s", shown)
	if code == 0 || !strings.Contains(shown, `this sign-in is the account and organisation your login "work" is signed in to, so it was not added; sign in to another account to add a login`) {
		t.Fatalf("a second sign-in to the same account = %d:\n%s", code, shown)
	}
	shown, code = ada.onTerminal(nil, "agent", "login", "claude", "--as", "default")
	t.Logf("$ cloudfield agent login claude --as default\n%s", shown)
	if code == 0 || !strings.Contains(shown, `"default" names the login in your home; choose another name`) {
		t.Fatalf("--as default = %d:\n%s", code, shown)
	}

	if out := logins(); out != "#  LOGIN    KIND          PLAN  EMAIL                  ORGANISATION  USAGE   AUTOMATIC  SHARES  BILLED  YOUR SESSIONS\n"+
		"1  default  -             -     -                      -             5h 12%  on         on      -       0\n"+
		"2  work     subscription  max   ada@work.example.test  Test Org      5h 40%  on         on      -       0\n" {
		t.Fatalf("agent logins after adding work:\n%s", out)
	}

	for _, step := range []struct {
		args []string
		out  string
	}{
		{[]string{"rename", "work", "job"}, "renamed login work to job\n"},
		{[]string{"order", "job", "default"}, "login order: 1 job, 2 default\n"},
		{[]string{"auto", "job", "off"}, "login job: used only when you name it with cloudfield session --login job\n"},
		{[]string{"share", "job", "off"}, "login job: only your own sessions run on it; Cloudfield moves teammates' sessions off it, and stops those no other login can run\n"},
	} {
		out, stderr, code := ada.cli(append([]string{"agent", "logins"}, step.args...)...)
		t.Logf("$ cloudfield agent logins %s\n%s%s", strings.Join(step.args, " "), out, stderr)
		if code != 0 || out != step.out {
			t.Fatalf("agent logins %v = %d %q %q; want %q", step.args, code, out, stderr, step.out)
		}
	}
	if out := logins(); out != "#  LOGIN    KIND          PLAN  EMAIL                  ORGANISATION  USAGE   AUTOMATIC  SHARES  BILLED  YOUR SESSIONS\n"+
		"1  job      subscription  max   ada@work.example.test  Test Org      5h 40%  off        off     -       0\n"+
		"2  default  -             -     -                      -             5h 12%  on         on      -       0\n" {
		t.Fatalf("agent logins after rename, order and auto:\n%s", out)
	}

	for _, refused := range []struct {
		args []string
		err  string
	}{
		{[]string{"rename", "nope", "x"}, `you have no login named "nope"; cloudfield agent logins lists yours`},
		{[]string{"order", "job"}, "login_ids must name each of your logins once, default included"},
		{[]string{"rename", "job", "Bad Name"}, `login name "Bad Name": use lowercase letters, digits and '-', starting with a letter or digit`},
		{[]string{"remove", "default", "--yes"}, "the default login is the one in your home; it is not renamed or removed here"},
		{[]string{"remove", "job"}, "--yes is required because cloudfield agent logins remove deletes the login's files from Cloudfield: cloudfield agent logins remove job --yes"},
	} {
		_, stderr, code := ada.cli(append([]string{"agent", "logins"}, refused.args...)...)
		t.Logf("$ cloudfield agent logins %s\n%s", strings.Join(refused.args, " "), stderr)
		if code == 0 || !strings.Contains(stderr, refused.err) {
			t.Errorf("agent logins %v = %d %q; want %q", refused.args, code, stderr, refused.err)
		}
	}

	shown, code = ada.onTerminal(map[string]string{"Remove? [y/N]: ": "y\r"}, "agent", "logins", "remove", "job")
	t.Logf("$ cloudfield agent logins remove job\n%s", shown)
	if code != 0 || !strings.HasPrefix(shown, "login job: subscription, plan max, ada@work.example.test, organisation Test Org\n"+
		"Removing it deletes its files from Cloudfield and does not sign it out at Anthropic; Cloudfield refuses while a session is on it. Remove? [y/N]: ") ||
		!strings.HasSuffix(shown, "removed login job and its files from Cloudfield; this does not sign it out at Anthropic\n") {
		t.Fatalf("agent logins remove job on a terminal = %d:\n%s", code, shown)
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("rows after removing the last added login: %+v", got)
	}

	// A terminal closed during the sign-in: the line printed before it
	// opened names the command that returns to it, and a second add is
	// refused with the server's words and how the first one ends.
	rt.mu.Lock()
	rt.signIn = saveAs("ada@later.example.test", "org-later")
	rt.mu.Unlock()
	shown = ada.closedAt("Paste code here", "agent", "login", "claude", "--as", "later")
	t.Logf("$ cloudfield agent login claude --as later   (terminal closed at the prompt)\n%s", shown)
	attach := regexp.MustCompile(`return to it with: (cloudfield agent login claude --attach (lgn-[a-z0-9]+))\n`).FindStringSubmatch(shown)
	if attach == nil {
		t.Fatalf("agent login claude --as later printed no command to return to the sign-in:\n%s", shown)
	}
	shown, code = ada.onTerminal(nil, "agent", "login", "claude", "--as", "other")
	t.Logf("$ cloudfield agent login claude --as other\n%s", shown)
	if code == 0 || !strings.Contains(shown, `the sign-in of login "later" (`+attach[2]+`) is still running; finish it first`+"\n"+
		"that sign-in ends by itself 15 minutes after its terminal opened, or 10 minutes after it started if none did; "+
		"to finish it sooner, return to it with:\n  "+attach[1]+"\n") {
		t.Fatalf("a second add while one runs = %d:\n%s", code, shown)
	}
	rt.mu.Lock()
	typed := len(rt.codes)
	rt.mu.Unlock()
	shown, code = ada.onTerminal(map[string]string{"Paste code here": "test-reattach-code\r"}, "agent", "login", "claude", "--attach", attach[2])
	t.Logf("$ %s\n%s", attach[1], shown)
	if code != 0 || !strings.Contains(shown, "added login later: subscription, plan max, ada@later.example.test, organisation Test Org\n"+
		"Automatic is on: Cloudfield may start or move your sessions onto it; turn it off with: cloudfield agent logins auto later off\n"+
		"Use for shares is on: teammates you share your account with can be served by it; turn it off with: cloudfield agent logins share later off\n") {
		t.Fatalf("%s = %d:\n%s", attach[1], code, shown)
	}
	rt.mu.Lock()
	if len(rt.codes) != typed+1 || rt.codes[typed] != "test-reattach-code" {
		t.Errorf("the sign-in returned to read %q after %d codes; want the code typed on the reattached terminal", rt.codes, typed)
	}
	rt.mu.Unlock()
	shown, code = ada.onTerminal(nil, "agent", "login", "claude", "--attach", attach[2])
	t.Logf("$ %s   (again, after it ended)\n%s", attach[1], shown)
	if code != 0 || shown != "returning to the sign-in of login "+attach[2]+": claude auth login runs in a container of its own; follow its prompts\n"+
		"added login later: subscription, plan max, ada@later.example.test, organisation Test Org\n"+
		"Automatic is on: Cloudfield may start or move your sessions onto it; turn it off with: cloudfield agent logins auto later off\n"+
		"Use for shares is on: teammates you share your account with can be served by it; turn it off with: cloudfield agent logins share later off\n" {
		t.Fatalf("%s after the sign-in ended = %d:\n%s", attach[1], code, shown)
	}
}

// TestIntegrationSessionOnANamedLogin launches through the CLI on a login
// named by --login and lists it with cloudfield sessions; a session on the
// default login lists its account as before.
func TestIntegrationSessionOnANamedLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubLoginUsage{used: map[domain.Account]float64{}}
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	second := domain.LoginID("lgn-second0000")
	addLogin(ctx, t, srv, e.dataDir, e.ada.id, second, "second")
	signInDefault(t, e.dataDir, e.ada.id)
	aether := buildCLIBinary(t)
	ada := newCLIUser(t, aether, srv.addr, e.keyPath)

	_, stderr, code := ada.cli("session", "on no login", "--agent", "claude", "--mode", "background", "--login", "nope")
	if code == 0 || !strings.Contains(stderr, `--login: you have no login named "nope"; cloudfield agent logins lists yours`) {
		t.Fatalf("session --login nope = %d %q", code, stderr)
	}
	if _, err := srv.srv.Store().ShareAccount(ctx, e.bo.id, e.ada.id); err != nil {
		t.Fatal(err)
	}
	for _, named := range []string{"nope", "second"} {
		_, stderr, code = ada.cli("session", "on Bo's login", "--agent", "claude", "--mode", "background", "--account", string(e.bo.id), "--login", named)
		t.Logf("$ cloudfield session \"on Bo's login\" --agent claude --mode background --account %s --login %s\n%s", e.bo.id, named, stderr)
		if code == 0 || !strings.Contains(stderr, "run.launch: only a login's owner names it; a launch on a teammate's account is served in their order") {
			t.Fatalf("session --account bo --login %s = %d %q; want the server's refusal", named, code, stderr)
		}
	}
	_, stderr, code = ada.cli("session", "on my own account", "--agent", "claude", "--mode", "background", "--account", string(e.ada.id), "--login", "nope")
	if code == 0 || !strings.Contains(stderr, `--login: you have no login named "nope"; cloudfield agent logins lists yours`) {
		t.Fatalf("session --account ada --login nope = %d %q", code, stderr)
	}
	out, stderr, code := ada.cli("session", "on the second login", "--agent", "claude", "--mode", "background", "--login", "second")
	t.Logf("$ cloudfield session \"on the second login\" --agent claude --mode background --login second\n%s%s", out, stderr)
	if code != 0 {
		t.Fatalf("session --login second = %d %q %q", code, out, stderr)
	}
	onSecond := strings.Fields(out)[1]
	ctrl, client := srv.control(t, e.ada.key)
	defer func() { _ = client.Close() }()
	onDefault := e.launch(t, ctrl, "on the default login", "claude").ID

	out, stderr, code = ada.cli("agent", "logins", "--json")
	t.Logf("$ cloudfield agent logins --json\n%s", out)
	var listed []struct {
		ID       string   `json:"id"`
		Sessions []string `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &listed); code != 0 || err != nil {
		t.Fatalf("agent logins --json = %d %q %q: %v", code, out, stderr, err)
	}
	on := map[string][]string{}
	for _, l := range listed {
		on[l.ID] = l.Sessions
	}
	if !reflect.DeepEqual(on, map[string][]string{"default": {onDefault}, string(second): {onSecond}}) {
		t.Errorf("sessions per login = %v; want %s on default and %s on second", on, onDefault, onSecond)
	}

	out, stderr, code = ada.cli("sessions")
	t.Logf("$ cloudfield sessions\n%s", out)
	if code != 0 {
		t.Fatalf("sessions = %d %q", code, stderr)
	}
	listedRow := map[string]string{}
	for _, row := range strings.Split(out, "\n") {
		if fields := strings.Fields(row); len(fields) > 0 {
			listedRow[fields[0]] = row
		}
	}
	if row, ok := listedRow[onSecond]; !ok || !strings.Contains(row, " Ada, login second ") {
		t.Errorf("the session on login second lists %q (listed: %v); want its account and login", row, ok)
	}
	if row, ok := listedRow[onDefault]; !ok || len(strings.Fields(row)) < 5 || strings.Fields(row)[4] != "Ada" || strings.Contains(row, "Ada, login") {
		t.Errorf("the session on the default login lists %q (listed: %v); want its account as before", row, ok)
	}

	// A named login at its usage limit: the launch falls back to the
	// default login, and the CLI says where it started.
	usage.set(domain.Account{Member: e.ada.id, Login: second}, 100)
	out, stderr, code = ada.cli("session", "on a spent login", "--agent", "claude", "--mode", "background", "--login", "second")
	t.Logf("$ cloudfield session \"on a spent login\" --agent claude --mode background --login second\n%s%s", out, stderr)
	if lines := strings.Split(out, "\n"); code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "session run-") ||
		lines[1] != "started on your login default, not second" || lines[2] != "" {
		t.Fatalf("session --login second at its usage limit = %d %q %q; want where it started", code, out, stderr)
	}
}
