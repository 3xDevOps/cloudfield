package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const (
	defaultLoginFile = `{"claudeAiOauth":{"accessToken":"test-access-default","refreshToken":"test-refresh-default","expiresAt":4102444800000,"subscriptionType":"pro"}}`
	addedLoginFile   = `{"claudeAiOauth":{"accessToken":"test-access-two","refreshToken":"test-refresh-two","expiresAt":4102444800000,"subscriptionType":"max"}}`
)

func cachedAccount(email, org string) string {
	return `{"numStartups":3,"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":"` + org + `","organizationName":"Test Org"}}`
}

// signInAttachment is the terminal of a stand-in for the agent's sign-in
// command: what it saves is written by the test, and closing it ends the
// command.
type signInAttachment struct {
	pr *io.PipeReader
	pw *io.PipeWriter
}

func newSignInAttachment() *signInAttachment {
	pr, pw := io.Pipe()
	return &signInAttachment{pr: pr, pw: pw}
}

func (a *signInAttachment) Stdin() io.WriteCloser                    { return nopWriteCloser{io.Discard} }
func (a *signInAttachment) Stdout() io.Reader                        { return a.pr }
func (a *signInAttachment) Stderr() io.Reader                        { return bytes.NewReader(nil) }
func (a *signInAttachment) Resize(context.Context, uint, uint) error { return nil }
func (a *signInAttachment) Close() error                             { return a.pw.Close() }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type signInEnv struct {
	*testEnv
	logins *memberhome.Logins
	home   string
}

func newSignInEnv(t *testing.T) *signInEnv {
	t.Helper()
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Logins = logins })
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, ".claude/.credentials.json", defaultLoginFile)
	writeHomeFile(t, home, ".claude.json", cachedAccount("ada@example.test", "org-default"))
	return &signInEnv{testEnv: e, logins: logins, home: home}
}

// signIn runs a sign-in of a login named name whose stand-in command, given
// the container's spec, saves what save writes, and returns its result.
func (e *signInEnv) signIn(t *testing.T, name string, save func(spec runtime.Spec)) (domain.LoginID, domain.AgentLogin, error) {
	t.Helper()
	ctx := t.Context()
	id, err := e.sched.BeginLoginSignIn(ctx, e.member.ID, "claude", name)
	if err != nil {
		t.Fatal(err)
	}
	att := newSignInAttachment()
	e.rt.mu.Lock()
	e.rt.execTTYHook = func(_ context.Context, cid runtime.ID, argv []string, _ string, _, _ uint) (runtime.Attachment, error) {
		if !slices.Equal(argv, []string{"claude", "auth", "login"}) {
			t.Errorf("sign-in argv = %q; want claude auth login", argv)
		}
		c, getErr := e.rt.get(cid)
		if getErr != nil {
			return nil, getErr
		}
		go func() {
			save(c.spec)
			_ = att.pw.Close()
		}()
		return att, nil
	}
	e.rt.mu.Unlock()
	if _, err = e.sched.StartLoginSignIn(ctx, e.member.ID, id, 80, 24); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	login, err := e.sched.LoginSignInResult(waitCtx, e.member.ID, "claude", id)
	return id, login, err
}

// hostPathAt is the host path mounted at the home-relative rel.
func hostPathAt(t *testing.T, spec runtime.Spec, rel string) string {
	t.Helper()
	target := path.Join(spec.Env["HOME"], rel)
	for _, m := range spec.Mounts {
		if m.ContainerPath == target {
			if m.Subpath != "" {
				return filepath.Join(m.HostPath, m.Subpath)
			}
			return m.HostPath
		}
	}
	t.Fatalf("nothing mounted at %s; mounts %+v", target, spec.Mounts)
	return ""
}

// A sign-in runs the agent's own command in a container of its own whose
// three login stores are the new login's directory, and adds the login it
// saved. The default login's files are never written, and nothing of the
// login reaches the result.
func TestSignInAddsTheLoginItSavedAndTouchesNoOtherLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-server-key")
	e := newSignInEnv(t)
	var dir string
	id, login, err := e.signIn(t, "work", func(spec runtime.Spec) {
		var pathErr error
		dir, pathErr = e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		if pathErr != nil {
			t.Error(pathErr)
			return
		}
		for rel, want := range map[string]string{
			".claude/.credentials.json": filepath.Join(dir, memberhome.LoginCredentials),
			".config/anthropic":         filepath.Join(dir, memberhome.LoginAnthropic),
			".claude/.claude.json":      filepath.Join(dir, memberhome.LoginConfig),
			".claude/backups":           filepath.Join(dir, memberhome.LoginBackups),
		} {
			if got := hostPathAt(t, spec, rel); got != want {
				t.Errorf("~/%s is mounted from %s; want %s", rel, got, want)
			}
		}
		for _, m := range spec.Mounts {
			if strings.HasPrefix(m.HostPath, e.logins.Root()) && !strings.HasPrefix(m.HostPath, dir) {
				t.Errorf("the sign-in mounts %s, another login's", m.HostPath)
			}
		}
		if spec.Env["CLAUDE_CONFIG_DIR"] != path.Join(spec.Env["HOME"], ".claude") || spec.Env["ANTHROPIC_CONFIG_DIR"] != path.Join(spec.Env["HOME"], ".config/anthropic") {
			t.Errorf("env CLAUDE_CONFIG_DIR %q ANTHROPIC_CONFIG_DIR %q; want both at the mounted stores", spec.Env["CLAUDE_CONFIG_DIR"], spec.Env["ANTHROPIC_CONFIG_DIR"])
		}
		if key, ok := spec.Env["ANTHROPIC_API_KEY"]; !ok || key != "" || spec.Sudo {
			t.Errorf("the sign-in container has ANTHROPIC_API_KEY %q (set %v) and sudo %v; want it set empty, over any image ENV, and no sudo", key, ok, spec.Sudo)
		}
		for _, m := range spec.Mounts {
			if m.ContainerPath == "/aether-cache" {
				t.Error("the sign-in container mounts a cache pool")
			}
			if m.ContainerPath == path.Join(spec.Env["HOME"], ".claude.json") && !m.ReadOnly {
				t.Error("the home's ~/.claude.json is writable in the sign-in container")
			}
		}
		config, readErr := os.ReadFile(filepath.Join(dir, memberhome.LoginConfig))
		if readErr != nil || strings.Contains(string(config), "oauthAccount") {
			t.Errorf("the sign-in's configuration copy = %s, %v; want the member's without the account", config, readErr)
		}
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@work.example.test", "org-work"))
		writeHomeFile(t, dir, "backups/.claude.json.backup", cachedAccount("ada@work.example.test", "org-work"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if login.ID != id || login.Name != "work" || !login.Automatic || !login.Shared {
		t.Fatalf("login = %+v; want work, automatic and shared", login)
	}
	logins, err := e.db.ListAgentLogins(t.Context(), e.member.ID, "claude")
	if err != nil || len(logins) != 2 || logins[0].ID != domain.DefaultLogin || logins[1].ID != id {
		t.Fatalf("logins = %+v, %v; want default then work", logins, err)
	}
	identity, err := e.logins.Identity(e.member.ID, "claude", id)
	if err != nil || identity != (memberhome.LoginIdentity{Kind: memberhome.LoginSubscription, Plan: "max", Email: "ada@work.example.test", OrganizationID: "org-work", OrganizationName: "Test Org"}) {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	for _, gone := range []string{memberhome.LoginConfig, memberhome.LoginBackups} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s after the sign-in: %v; want it gone", gone, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(e.home, ".claude/.credentials.json")); string(data) != defaultLoginFile {
		t.Fatalf("the default login after the sign-in = %s; want it untouched", data)
	}
	if e.rt.byName("aether-login-"+string(id)) != nil {
		t.Fatal("the sign-in's container outlived it")
	}
}

// A sign-in that saved a Console API key, saved nothing, or signed in to the
// account of a login the member already has adds no login and leaves no
// directory.
func TestSignInRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		save func(dir string)
		want string
	}{
		{"api key", func(dir string) {
			writeHomeFile(t, dir, memberhome.LoginConfig, `{"primaryApiKey":"test-not-a-key"}`)
		}, "Console API key"},
		{"nothing", func(string) {}, "before Claude Code saved a login"},
		{"same account as the default", func(dir string) {
			writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
			writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@example.test", "org-default"))
		}, `your login "default"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSignInEnv(t)
			id, _, err := e.signIn(t, "work", func(spec runtime.Spec) {
				dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
				tc.save(dir)
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "test-not-a-key") || strings.Contains(err.Error(), "test-access") {
				t.Fatalf("sign-in error = %v; want one naming %q and quoting no credential", err, tc.want)
			}
			dir, _ := e.logins.Path(e.member.ID, "claude", id)
			if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("the refused login's directory: %v; want it removed", statErr)
			}
			if logins, listErr := e.db.ListAgentLogins(t.Context(), e.member.ID, "claude"); listErr != nil || len(logins) != 0 {
				t.Fatalf("logins after a refused sign-in = %+v, %v; want no row", logins, listErr)
			}
		})
	}
}

// A sign-in another member reserved cannot be started or read by this one.
func TestSignInIsTheMembersOwn(t *testing.T) {
	e := newSignInEnv(t)
	id, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.StartLoginSignIn(t.Context(), "mem-other", id, 80, 24); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member starting the sign-in: %v; want not found", err)
	}
	if _, err := e.sched.LoginSignInResult(t.Context(), "mem-other", "claude", id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member reading the result: %v; want not found", err)
	}
	if _, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "codex", "work"); !errors.Is(err, ErrNoAddedLogins) {
		t.Fatalf("an added Codex login: %v; want ErrNoAddedLogins", err)
	}
}

// A member who adds no login gets no row and no file from a launch, and a
// server restart's reconciliation creates none either.
func TestAMemberWithoutAddedLoginsGetsNoRowOrFile(t *testing.T) {
	e := newSignInEnv(t)
	run, _ := e.launchFake(t, "work")
	if run.AccountLogin != "" {
		t.Fatalf("run account login = %q; want the default login", run.AccountLogin)
	}
	if err := e.sched.ReconcileLogins(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(e.logins.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("logins root after a launch: %v; want none created", err)
	}
	if logins, err := e.db.ListAgentLogins(t.Context(), e.member.ID, "claude"); err != nil || len(logins) != 0 {
		t.Fatalf("logins after a launch = %+v, %v; want no row", logins, err)
	}
}

// At start a login directory with no row, left by a sign-in the server
// stopped during, goes; one with a row stays.
func TestReconcileRemovesOnlyLoginsWithoutARow(t *testing.T) {
	e := newSignInEnv(t)
	kept, _, err := e.signIn(t, "work", func(spec runtime.Spec) {
		dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@work.example.test", "org-work"))
	})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := e.logins.Add(e.member.ID, "claude", "lgn-orphan0000")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReconcileLogins(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan login directory: %v; want it removed", err)
	}
	keptDir, _ := e.logins.Path(e.member.ID, "claude", kept)
	if _, err := os.Lstat(keptDir); err != nil {
		t.Fatalf("added login's directory: %v; want it kept", err)
	}
}

// A member runs one sign-in at a time, and one that ended frees its name at
// once: a new sign-in under it starts.
func TestASignInThatEndedFreesItsName(t *testing.T) {
	e := newSignInEnv(t)
	if _, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "work"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second sign-in while one is pending: %v; want a conflict", err)
	}
	if err := e.sched.EndLoginSignIns(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.signIn(t, "work", func(runtime.Spec) {}); err == nil || !strings.Contains(err.Error(), "before Claude Code saved a login") {
		t.Fatalf("an empty sign-in: %v", err)
	}
	if _, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "work"); err != nil {
		t.Fatalf("a sign-in under the name of one that ended: %v", err)
	}
}

// When the sign-in's container cannot be removed, nothing is added and its
// directory stays, so the next start finds the container by it; a later
// reconcile removes both once the container goes.
func TestASignInWhoseContainerStaysAddsNothing(t *testing.T) {
	e := newSignInEnv(t)
	removeErr := errors.New("test: destroy unavailable")
	var dir string
	id, _, err := e.signIn(t, "work", func(spec runtime.Spec) {
		dir, _ = e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@work.example.test", "org-work"))
		e.rt.mu.Lock()
		e.rt.destroyErr = removeErr
		e.rt.mu.Unlock()
	})
	if err == nil || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("sign-in error = %v; want the kept container named", err)
	}
	if logins, _ := e.db.ListAgentLogins(t.Context(), e.member.ID, "claude"); len(logins) != 0 {
		t.Fatalf("logins = %+v; want none added", logins)
	}
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Fatalf("the directory of a sign-in whose container lives: %v; want it kept", statErr)
	}
	if err := e.sched.ReconcileLogins(t.Context(), ""); err == nil {
		t.Fatal("reconcile reported nothing while the container stays")
	}
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Fatalf("after a failed reconcile the directory: %v; want it kept", statErr)
	}
	e.rt.mu.Lock()
	e.rt.destroyErr = nil
	e.rt.mu.Unlock()
	if err := e.sched.ReconcileLogins(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) || e.rt.byName("aether-login-"+string(id)) != nil {
		t.Fatalf("after the container went: directory %v; want both gone", statErr)
	}
}

// A sign-in that ends after its member was removed adds nothing.
func TestASignInOfARemovedMemberAddsNothing(t *testing.T) {
	e := newSignInEnv(t)
	_, _, err := e.signIn(t, "work", func(spec runtime.Spec) {
		dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@work.example.test", "org-work"))
		if delErr := e.db.DeleteMember(t.Context(), e.member.ID, ""); delErr != nil {
			t.Error(delErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "removed or merged") {
		t.Fatalf("sign-in error = %v; want the member gone", err)
	}
}

// A sign-in whose login id already has a directory removes nothing of it.
func TestASignInNeverRemovesADirectoryItDidNotCreate(t *testing.T) {
	e := newSignInEnv(t)
	id, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.logins.Add(e.member.ID, "claude", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.StartLoginSignIn(t.Context(), e.member.ID, id, 80, 24); err == nil {
		t.Fatal("a sign-in over an existing directory started")
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("the existing directory: %v; want it kept", err)
	}
}

// The duplicate check fails closed: a login whose identity cannot be read
// refuses the sign-in rather than letting a duplicate in.
func TestTheDuplicateCheckFailsClosed(t *testing.T) {
	e := newSignInEnv(t)
	first, _, err := e.signIn(t, "work", func(spec runtime.Spec) {
		dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@work.example.test", "org-work"))
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDir, _ := e.logins.Path(e.member.ID, "claude", first)
	writeHomeFile(t, firstDir, "identity.json", "{torn")
	_, _, err = e.signIn(t, "spare", func(spec runtime.Spec) {
		dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
		writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
		writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount("ada@spare.example.test", "org-spare"))
	})
	if err == nil || !strings.Contains(err.Error(), `your login "work" could not be read`) {
		t.Fatalf("sign-in with an unreadable login beside it: %v; want refused", err)
	}
}

// A sign-in whose member was removed before its terminal opened recreates
// neither a home nor a login directory.
func TestASignInOfAMemberRemovedBeforeItStartsMakesNothing(t *testing.T) {
	e := newSignInEnv(t)
	other := &domain.Member{DisplayName: "Bo", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	id, err := e.sched.BeginLoginSignIn(t.Context(), other.ID, "claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.DeleteMember(t.Context(), other.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.StartLoginSignIn(t.Context(), other.ID, id, 80, 24); err == nil {
		t.Fatal("the sign-in of a removed member started")
	}
	dir, _ := e.logins.Path(other.ID, "claude", id)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the removed member's login directory: %v; want none", err)
	}
	if has, err := e.cfg.Homes.HasHome(other.ID); err != nil || has {
		t.Fatalf("the removed member's home exists = %v, %v; want none", has, err)
	}
}

// A sign-in whose member is removed after its start read the member, while
// its container is made, stops before the sign-in command runs and leaves
// no login directory.
func TestASignInOfAMemberRemovedWhileItStartsRunsNothing(t *testing.T) {
	e := newSignInEnv(t)
	other := &domain.Member{DisplayName: "Bo", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	id, err := e.sched.BeginLoginSignIn(t.Context(), other.ID, "claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.createHook = func() {
		if delErr := e.db.DeleteMember(t.Context(), other.ID, ""); delErr != nil {
			t.Error(delErr)
		}
	}
	e.rt.execTTYHook = func(context.Context, runtime.ID, []string, string, uint, uint) (runtime.Attachment, error) {
		t.Error("the sign-in command ran for a removed member")
		return newSignInAttachment(), nil
	}
	e.rt.mu.Unlock()
	if _, err := e.sched.StartLoginSignIn(t.Context(), other.ID, id, 80, 24); err == nil || !strings.Contains(err.Error(), "removed or merged") {
		t.Fatalf("the sign-in of a member removed while it started: %v; want it stopped", err)
	}
	dir, _ := e.logins.Path(other.ID, "claude", id)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the removed member's login directory: %v; want none", err)
	}
}

// When a removed member's running sign-in cannot be ended, reconciling
// the member's logins keeps that sign-in's directory, by which the next
// start finds its container, and removes the settled logins.
func TestReconcileAfterASignInCouldNotEndKeepsOnlyItsDirectory(t *testing.T) {
	e := newSignInEnv(t)
	ctx := t.Context()
	settled, err := e.logins.Add(e.member.ID, "claude", "lgn-settled000")
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, settled, memberhome.LoginCredentials, addedLoginFile)
	if err := e.logins.Settle(e.member.ID, "claude", "lgn-settled000", memberhome.LoginIdentity{Kind: memberhome.LoginSubscription}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: e.member.ID, Agent: "claude", ID: "lgn-settled000", Name: "settled", CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
	id, err := e.sched.BeginLoginSignIn(ctx, e.member.ID, "claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.execTTYHook = func(context.Context, runtime.ID, []string, string, uint, uint) (runtime.Attachment, error) {
		return newSignInAttachment(), nil
	}
	e.rt.mu.Unlock()
	if _, err := e.sched.StartLoginSignIn(ctx, e.member.ID, id, 80, 24); err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.destroyErr = errors.New("test: destroy unavailable")
	e.rt.mu.Unlock()
	if err := e.db.DeleteMember(ctx, e.member.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.EndLoginSignIns(ctx, e.member.ID); err == nil {
		t.Fatal("ending a sign-in whose container cannot be removed reported nothing")
	}
	if err := e.sched.ReconcileLogins(ctx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(settled); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the removed member's settled login: %v; want it gone", err)
	}
	signIn, _ := e.logins.Path(e.member.ID, "claude", id)
	if _, err := os.Lstat(signIn); err != nil {
		t.Fatalf("the directory of the sign-in whose container lives: %v; want it kept", err)
	}
}

// A login row whose directory is gone holds nothing to duplicate, so it
// does not block the next sign-in.
func TestALoginWithoutADirectoryDoesNotBlockASignIn(t *testing.T) {
	e := newSignInEnv(t)
	save := func(email string) func(runtime.Spec) {
		return func(spec runtime.Spec) {
			dir, _ := e.logins.Path(e.member.ID, "claude", domain.LoginID(strings.TrimPrefix(spec.Name, "aether-login-")))
			writeHomeFile(t, dir, memberhome.LoginCredentials, addedLoginFile)
			writeHomeFile(t, dir, memberhome.LoginConfig, cachedAccount(email, "org-"+email))
		}
	}
	first, _, err := e.signIn(t, "work", save("ada@work.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.logins.Remove(e.member.ID, "claude", first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.signIn(t, "spare", save("ada@spare.example.test")); err != nil {
		t.Fatalf("a sign-in beside a login whose directory is gone: %v", err)
	}
}
