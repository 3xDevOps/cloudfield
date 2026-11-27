package scheduler

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/secretstore"
	acp "github.com/coder/acp-go-sdk"
)

type loginShareEnv struct {
	*shareEnv
	logins *memberhome.Logins
}

func newLoginShareEnv(t *testing.T) *loginShareEnv {
	t.Helper()
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	e := newShareEnv(t, func(c *Config) { c.Logins = logins })
	writeHomeFile(t, e.adaHome, ".claude.json", `{"primaryApiKey":"test-not-a-key-of-ada","numStartups":2}`)
	return &loginShareEnv{shareEnv: e, logins: logins}
}

// addLogin settles an added login of owner as a finished sign-in would;
// file is its credentials, or "" for a keyless Console login.
func (e *loginShareEnv) addLogin(t *testing.T, owner domain.MemberID, id domain.LoginID, name, file string) domain.Account {
	t.Helper()
	dir, err := e.logins.Add(owner, "claude", id)
	if err != nil {
		t.Fatal(err)
	}
	identity := memberhome.LoginIdentity{Kind: memberhome.LoginSubscription, Plan: "max"}
	if file == "" {
		identity = memberhome.LoginIdentity{Kind: memberhome.LoginConsole}
		writeHomeFile(t, dir, "anthropic/configs/default.json", `{"authentication":{"type":"user_oauth"}}`)
		writeHomeFile(t, dir, "anthropic/credentials/default.json", `{"access_token":"test-console-`+name+`"}`)
	} else {
		writeHomeFile(t, dir, memberhome.LoginCredentials, file)
	}
	if err := e.logins.Settle(owner, "claude", id, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.AddAgentLogin(t.Context(), domain.AgentLogin{MemberID: owner, Agent: "claude", ID: id, Name: name, CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
	return domain.Account{Member: owner, Login: id}
}

func (e *loginShareEnv) launchOn(t *testing.T, account domain.Account, permission domain.Permission) (*domain.Run, error) {
	t.Helper()
	return e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, account.Member, "use "+string(account.Login), "claude", domain.LaunchTUI,
		domain.LaunchOptions{AccountLogin: account.Login, Permission: permission})
}

const workLogin = `{"claudeAiOauth":{"accessToken":"test-access-work","refreshToken":"test-refresh-work","expiresAt":4102444800000}}`

// A teammate's session on an added login the owner shared mounts that
// login from the owner's login directory, as checked subpaths, and nothing
// of the owner's default login; the launcher's configuration copy carries
// no key, and the legacy configuration is masked though the launcher has
// none.
func TestATeammatesSessionMountsTheSharedAddedLogin(t *testing.T) {
	e := newLoginShareEnv(t)
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	if _, err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	spec := e.rt.byName(string(run.ID)).spec
	dir, _ := e.logins.Path(e.owner.ID, "claude", work.Login)
	home := spec.Env["HOME"]
	want := map[string]string{path.Join(home, ".claude/.credentials.json"): memberhome.LoginCredentials, path.Join(home, ".config/anthropic"): memberhome.LoginAnthropic}
	for _, m := range spec.Mounts {
		if sub, ok := want[m.ContainerPath]; ok {
			if m.HostPath != dir || m.Subpath != sub {
				t.Errorf("%s is mounted from %s/%s; want %s/%s", m.ContainerPath, m.HostPath, m.Subpath, dir, sub)
			}
			delete(want, m.ContainerPath)
		}
		if m.HostPath == e.ownerHome {
			t.Errorf("the session mounts %s from Grace's home", m.Subpath)
		}
	}
	if len(want) > 0 {
		t.Fatalf("not mounted: %v; mounts %+v", want, spec.Mounts)
	}
	// Ada has no legacy configuration; one written later must not win.
	if !slices.ContainsFunc(spec.Mounts, func(m runtime.Mount) bool {
		return m.ContainerPath == path.Join(home, ".claude/.config.json") && m.ReadOnly
	}) {
		t.Errorf("no read-only mask over ~/.claude/.config.json; mounts %+v", spec.Mounts)
	}
	if spec.Env["ANTHROPIC_CONFIG_DIR"] != path.Join(home, ".config/anthropic") {
		t.Errorf("ANTHROPIC_CONFIG_DIR = %q", spec.Env["ANTHROPIC_CONFIG_DIR"])
	}
	copy, err := os.ReadFile(filepath.Join(e.sched.accountConfigRoot(), string(run.ID), ".claude.json"))
	if err != nil || strings.Contains(string(copy), "test-not-a-key-of-ada") {
		t.Fatalf("the session's configuration copy = %s, %v; want Ada's without her key", copy, err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.LoginMember != string(e.owner.ID) || sc.LoginID != work.Login {
		t.Fatalf("sidecar login %q %q, %v; want Grace's work", sc.LoginMember, sc.LoginID, err)
	}
	startScheduler(t, e.sched)
	waitFor(t, "recovery", func() bool {
		select {
		case <-e.sched.recoveryReady:
			return true
		default:
			return false
		}
	})
	if inUse, err := e.sched.LoginInUse(t.Context(), work); err != nil || inUse[run.ID] != e.member.ID {
		t.Fatalf("LoginInUse = %v, %v; want the session", inUse, err)
	}
}

// A launch on an added login is refused when its owner turned it off for
// shares, when it cannot sign in, when an Isolated session would get a
// Console login's directory, and when a workspace variable would pay
// instead.
func TestLaunchOnAnAddedLoginRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       string
		own        bool
		unshared   bool
		permission domain.Permission
		variables  map[string]string
		want       error
		wantText   string
	}{
		{name: "off for shares", file: workLogin, unshared: true, want: permissions.ErrDenied},
		{name: "signed out", file: `{"claudeAiOauth":{"accessToken":"test-access-old","expiresAt":1}}`, want: ErrSignedOut, wantText: "cloudfield agent login claude --as work"},
		{name: "isolated Console", own: true, permission: domain.PermissionIsolated, wantText: "Isolated session cannot use a Console login"},
		{name: "a workspace key pays", own: true, file: workLogin, variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}, wantText: "would not pay"},
		{name: "Microsoft Foundry pays", own: true, file: workLogin, variables: map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "True"}, wantText: "would not pay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newLoginShareEnv(t)
			owner := e.owner.ID
			if tc.own {
				owner = e.member.ID
			}
			login := e.addLogin(t, owner, "lgn-work000000", "work", tc.file)
			if _, err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
				t.Fatal(err)
			}
			if tc.unshared {
				if err := e.db.SetAgentLoginShared(t.Context(), owner, "claude", login.Login, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.variables != nil {
				if err := e.db.SetWorkspaceEnvironment(t.Context(), e.ws.ID, domain.WorkspaceEnvironment{Variables: tc.variables}); err != nil {
					t.Fatal(err)
				}
			}
			before := e.containerCount()
			run, err := e.launchOn(t, login, tc.permission)
			if err == nil && run != nil {
				waitFor(t, "the refused launch to settle", func() bool {
					r, getErr := e.db.GetRun(t.Context(), run.ID)
					return getErr == nil && r.Status.Terminal()
				})
				r, _ := e.db.GetRun(t.Context(), run.ID)
				err = errors.New(r.Reason)
			}
			switch {
			case err == nil:
				t.Fatal("the launch was admitted")
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("launch: %v; want %v", err, tc.want)
			case tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText):
				t.Fatalf("launch: %v; want %q", err, tc.wantText)
			}
			if run == nil && e.containerCount() != before {
				t.Fatal("a refused launch created a container")
			}
		})
	}
}

// The login a launch that names none starts from is the account's first
// one the launcher may use; a teammate with none is not shared the account.
func TestStartLoginAndChildLogin(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", workLogin)
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	console := e.addLogin(t, e.owner.ID, "lgn-console000", "console", "")
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginShared(ctx, e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("Ada's start on Grace's account = %q, %v; want work, the first shared", got, err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Grace's own start = %q, %v; want her default", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, work.Login, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("a subsession of a session on work = %q, %v; want work inherited", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, console.Login, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("a subsession of a session on a Console login = %q, %v; want the first eligible, not the per-use login", got, err)
	}
	// A teammate's subsession that names the account is served in the
	// owner's order, never by a login the owner did not leave automatic.
	if err := e.db.SetAgentLoginShared(ctx, e.owner.ID, "claude", domain.DefaultLogin, true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("Ada's subsession naming Grace's account = %q, %v; want work", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, domain.DefaultLogin, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("Ada's subsession inheriting Grace's default, automatic off = %q, %v; want work", got, err)
	}
	for _, id := range []domain.LoginID{work.Login, console.Login} {
		if err := e.db.SetAgentLoginShared(ctx, e.owner.ID, "claude", id, false); err != nil {
			t.Fatal(err)
		}
	}
	// On her own account Grace with no automatic login is refused, never
	// launched on one she did not pick.
	if err := e.db.SetAgentLoginAutomatic(ctx, e.owner.ID, "claude", work.Login, false); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.owner.ID, "claude", console.Login, false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); !errors.Is(err, ErrNoStartLogin) {
		t.Fatalf("Grace's start with no automatic login = %q, %v; want refused", got, err)
	}
	if _, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("Ada's start with no shared login: %v; want denied", err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, work.Login, "claude", domain.LaunchTUI, ""); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("a subsession of Ada's on Grace's unshared login = %q, %v; want denied", got, err)
	}
	// A workspace variable that signs Claude Code in pays: no login is
	// chosen.
	if err := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("a start where a variable pays = %q, %v; want the default login", got, err)
	}
}

// A launch that names no login never lands on a login its owner turned
// automatic off because nothing else served: it is refused, naming each
// login and why, and the flag that names one. A member who added no login
// has no row, so nothing of theirs is off and the launch starts on the
// default login as before logins existed.
func TestALaunchNamingNoLoginNeverFallsBackToALoginThatIsOff(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	expired := `{"claudeAiOauth":{"accessToken":"test-access-old","expiresAt":1}}`
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", expired)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Grace's start with no added login and a signed-out default = %q, %v; want the default login, whose launch says why", got, err)
	}
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", expired)
	if err := e.db.OrderAgentLogins(ctx, e.member.ID, "claude", []domain.LoginID{work.Login, domain.DefaultLogin}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, "")
	if !errors.Is(err, ErrNoStartLogin) || !strings.Contains(err.Error(), ": work: ") || !strings.Contains(err.Error(), "default is automatic off; name one with cloudfield session --login <name>") {
		t.Fatalf("Ada's start with work signed out and her API-key default automatic off: %v; want refused naming both", err)
	}
	dir, _ := e.logins.Path(e.member.ID, "claude", work.Login)
	writeHomeFile(t, dir, memberhome.LoginCredentials, workLogin)
	if err = e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", work.Login, false); err != nil {
		t.Fatal(err)
	}
	_, err = e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, "")
	if !errors.Is(err, ErrNoStartLogin) || !strings.Contains(err.Error(), "work is automatic off; default is automatic off") || !strings.Contains(err.Error(), "cloudfield agent logins auto <name> on") {
		t.Fatalf("Ada's start with every login automatic off: %v; want refused naming both", err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", domain.DefaultLogin, true); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Ada's start with her default automatic again = %q, %v; want the default login", got, err)
	}
}

// A teammate who picks an account whose owner added logins may still run
// the owner's Claude Code installation, as before logins existed, on any of
// that account's logins; only a fallback on an account the launcher did not
// pick refuses to borrow it.
func TestATeammatesStartOnASharedAccountMayBorrowTheOwnersInstallation(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", workLogin)
	installClaude(t, e.ownerHome)
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Ada's start before Grace adds a login = %q, %v; want the default login", got, err)
	}
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Ada's start after Grace adds a login = %q, %v; want the default login, first in Grace's order", got, err)
	}
	if err := e.db.SetAgentLoginShared(ctx, e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, "")
	if err != nil || got != work.Login {
		t.Fatalf("Ada's start with Grace's default off for shares = %q, %v; want work", got, err)
	}
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	if spec := e.rt.byName(string(run.ID)).spec; !strings.HasSuffix(spec.Env["PATH"], ":/root/"+accountBin) {
		t.Fatalf("PATH = %q; want Grace's installation borrowed", spec.Env["PATH"])
	}
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.member.ID, work, "claude", domain.LaunchTUI, ""); err != nil || ok {
		t.Fatalf("work as a fallback Ada did not pick = %v, %v; want refused, as it borrows Grace's installation", ok, err)
	}
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.owner.ID, work, "claude", domain.LaunchTUI, ""); err != nil || !ok {
		t.Fatalf("work as a fallback on Grace's account, which Ada picked = %v, %v; want it, on the installation Ada chose to run", ok, err)
	}
}

// A login's other store is read-only to sessions on it, and a login whose
// files no longer hold what it signed in as is not mounted.
func TestAnAddedLoginIsMountedByItsShape(t *testing.T) {
	e := newLoginShareEnv(t)
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	spec := e.rt.byName(string(run.ID)).spec
	for _, m := range spec.Mounts {
		switch m.Subpath {
		case memberhome.LoginCredentials:
			if m.ReadOnly {
				t.Error("a subscription login's credentials are read-only; its refresh would fail")
			}
		case memberhome.LoginAnthropic:
			if !m.ReadOnly {
				t.Error("a subscription login's Anthropic profile directory is writable")
			}
		}
	}
	dir, _ := e.logins.Path(e.member.ID, "claude", work.Login)
	// Read while a session rewrites it in place, or after a sign-out, the
	// file holds nothing: the login is not to be removed for it.
	writeHomeFile(t, dir, memberhome.LoginCredentials, "")
	profile, _ := harness.Lookup("claude")
	gap := &domain.Run{ID: "run-gap", MemberID: e.member.ID, AccountMemberID: e.member.ID, AccountLogin: work.Login, Harness: "claude"}
	if _, err = e.sched.addedLoginMounts(gap, e.member.ID, profile, "/root", map[string]string{}, EnvironmentPurposeRun, false); !errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "launch again") || strings.Contains(err.Error(), "adds it again") {
		t.Fatalf("mounting a login whose file is empty: %v; want signed out right now, launch again", err)
	}
	if _, err = e.launchOn(t, work, ""); !errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "launch again") {
		t.Fatalf("a launch on a login whose file is empty: %v; want signed out right now, launch again", err)
	}
	writeHomeFile(t, dir, "anthropic/configs/default.json", `{"authentication":{"type":"user_oauth"}}`)
	writeHomeFile(t, dir, "anthropic/credentials/default.json", `{"access_token":"test-planted"}`)
	planted, err := e.launchOn(t, work, "")
	if err == nil {
		waitFor(t, "the planted launch to settle", func() bool {
			r, getErr := e.db.GetRun(t.Context(), planted.ID)
			return getErr == nil && r.Status.Terminal()
		})
		r, _ := e.db.GetRun(t.Context(), planted.ID)
		err = errors.New(r.Reason)
	}
	if err == nil || !strings.Contains(err.Error(), "no longer holds the subscription sign-in") {
		t.Fatalf("a launch on a login planted with another kind: %v; want refused", err)
	}
}

// A teammate whose shared logins are all signed out is told so, login by
// login, rather than that the account is not shared.
func TestATeammateIsToldWhyNoSharedLoginServes(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	expired := `{"claudeAiOauth":{"accessToken":"test-access-old","expiresAt":1}}`
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", expired)
	e.addLogin(t, e.owner.ID, "lgn-work000000", "work", expired)
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI, "")
	if !errors.Is(err, permissions.ErrDenied) || !strings.Contains(err.Error(), "work: ") || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "not shared with you") {
		t.Fatalf("Ada's start on Grace's signed-out logins: %v; want denied, naming work and why", err)
	}
}

// A server that runs Claude Code from an agent definition naming its own
// executable has no account configuration to copy, so an added login is
// refused with that reason at sign-in, at launch and in the session's plan.
func TestAnAgentDefinitionWithItsOwnExecutableCarriesNoAddedLogin(t *testing.T) {
	e := newLoginShareEnv(t)
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	e.sched.harnesses["claude"] = HarnessSpec{Executable: "claude", TUIArgs: []string{"claude", "{task}"}, CredentialPaths: []string{".claude/.credentials.json"}}
	const reason = "from its --agent-definitions entry, whose executable leaves no place for an added login; use the default login, or the server's operator removes executable from that entry"
	if _, err := e.sched.BeginLoginSignIn(t.Context(), e.member.ID, "claude", "spare"); err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("a sign-in under the definition: %v; want refused", err)
	}
	if _, err := e.launchOn(t, work, ""); err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("a launch on work under the definition: %v; want refused", err)
	}
	if err := e.db.SetAgentLoginAutomatic(t.Context(), e.member.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); !errors.Is(err, ErrNoStartLogin) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("Ada's start under the definition with her default automatic off = %q, %v; want refused, naming why work does not serve", got, err)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := e.db.GetWorkspace(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := e.sched.launchProfile(t.Context(), member.ID, member.ID, "claude")
	if err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "run-definition", MemberID: member.ID, AccountMemberID: member.ID, AccountLogin: work.Login, Harness: "claude"}
	if _, err := e.sched.BuildEnvironmentPlan(t.Context(), run, ws, member, profile, EnvironmentPurposeRun); err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("the plan of a session on work under the definition: %v; want refused", err)
	}
}

// The last check before a teammate's container starts asks for the
// login's switches as well as the share: a login its owner turned
// automatic off after the launch was admitted does not start.
func TestALoginTurnedOffAfterAdmissionDoesNotStart(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", workLogin)
	e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.LaunchWithOptions(ctx, e.ws.ID, e.member.ID, e.owner.ID, "t", "claude", domain.LaunchTUI, domain.LaunchOptions{
		Created: func(domain.RunID) {
			if err := e.db.SetAgentLoginAutomatic(ctx, e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
				t.Error(err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not available to you") {
		t.Fatalf("Ada's launch on Grace's default, turned automatic off once admitted: %v; want refused", err)
	}
	if n := e.containerCount(); n != 0 {
		t.Fatalf("%d containers after the refused launch; want none", n)
	}
}

// A session on an added login sets the variables that would sign Claude
// Code in empty, so an image's ENV cannot fill them, and its plan refuses
// the login when a workspace variable added after admission would pay.
func TestAnAddedLoginSessionLeavesNoVariableToPay(t *testing.T) {
	e := newLoginShareEnv(t)
	e.sched.cfg.Secrets = secretstore.New(t.TempDir())
	ctx := t.Context()
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	spec := e.rt.byName(string(run.ID)).spec
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_PROFILE"} {
		if v, ok := spec.Env[key]; !ok || v != "" {
			t.Errorf("%s = %q, set %v; want set empty", key, v, ok)
		}
	}
	_, err = e.sched.LaunchWithOptions(ctx, e.ws.ID, e.member.ID, e.member.ID, "t", "claude", domain.LaunchTUI, domain.LaunchOptions{
		AccountLogin: work.Login,
		Created: func(domain.RunID) {
			if setErr := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}}); setErr != nil {
				t.Error(setErr)
			}
		},
	})
	if !errors.Is(err, ErrEnvironmentPays) && (err == nil || !strings.Contains(err.Error(), "would not pay")) {
		t.Fatalf("a launch on work once a workspace key was added: %v; want refused", err)
	}
}

// A workspace switch Claude Code reads as off pays nothing, so it leaves a
// launch on an added login alone.
func TestAWorkspaceSwitchSetOffLeavesAnAddedLoginToPay(t *testing.T) {
	e := newLoginShareEnv(t)
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	if err := e.db.SetWorkspaceEnvironment(t.Context(), e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{
		"CLAUDE_CODE_USE_BEDROCK": "0", "CLAUDE_CODE_USE_VERTEX": "false", "CLAUDE_CODE_USE_FOUNDRY": "off",
	}}); err != nil {
		t.Fatal(err)
	}
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatalf("a launch on work with every provider switch off: %v", err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
	if v := e.rt.byName(string(run.ID)).spec.Env["CLAUDE_CODE_USE_BEDROCK"]; v != "0" {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK = %q; want the workspace's 0", v)
	}
}

// Choosing a login knows whether the session is Isolated, which cannot
// mount a Console login's directory, and skips a login whose files no
// longer hold the sign-in it was added with, rather than choosing either
// and failing the launch.
func TestChoosingALoginSkipsOneTheSessionCannotMount(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	console := e.addLogin(t, e.member.ID, "lgn-console000", "console", "")
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	if err := e.db.OrderAgentLogins(ctx, e.member.ID, "claude", []domain.LoginID{console.Login, work.Login, domain.DefaultLogin}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != console.Login {
		t.Fatalf("Ada's start = %q, %v; want console, her first", got, err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, domain.PermissionIsolated); err != nil || got != work.Login {
		t.Fatalf("Ada's Isolated start = %q, %v; want work, past the Console login", got, err)
	}
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.member.ID, console, "claude", domain.LaunchTUI, domain.PermissionIsolated); err != nil || ok {
		t.Fatalf("console as an Isolated fallback = %v, %v; want refused", ok, err)
	}
	dir, _ := e.logins.Path(e.member.ID, "claude", work.Login)
	writeHomeFile(t, dir, memberhome.LoginCredentials, "")
	writeHomeFile(t, dir, "anthropic/configs/default.json", `{"authentication":{"type":"user_oauth"}}`)
	writeHomeFile(t, dir, "anthropic/credentials/default.json", `{"access_token":"test-planted"}`)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, domain.PermissionIsolated); err != nil || got != "" {
		t.Fatalf("Ada's Isolated start with work planted with a Console sign-in = %q, %v; want the default login", got, err)
	}
}

// Beside added logins, a default login that holds no sign-in at all does
// not start a launch that names none: the next login does.
func TestADefaultLoginWithNoSignInIsPassedOver(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	if err := os.Remove(filepath.Join(e.adaHome, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("Ada's start with a default login that never signed in = %q, %v; want work", got, err)
	}
	// Claude Code signs in through ~/.claude/settings.json too, in ways
	// Cloudfield does not read: such a default login is not passed over.
	for _, settings := range []string{`{"apiKeyHelper":"/usr/local/bin/key-helper"}`, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`, `{"env":{"ANTHROPIC_API_KEY":"test-not-a-key"}}`} {
		writeHomeFile(t, e.adaHome, ".claude/settings.json", settings)
		if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
			t.Fatalf("Ada's start with settings %s = %q, %v; want the default login, her first", settings, got, err)
		}
	}
	writeHomeFile(t, e.adaHome, ".claude/settings.json", `{"env":{"CLAUDE_CODE_USE_BEDROCK":"0"}}`)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("Ada's start with Bedrock off in her settings = %q, %v; want work", got, err)
	}
	writeHomeFile(t, e.adaHome, ".claude/.credentials.json", workLogin)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("Ada's start once her default login signed in = %q, %v; want the default login, her first", got, err)
	}
}

// A default login that holds no sign-in is no fallback once anyone the
// launch involves keeps an added login, though its owner keeps none: its
// usage reads as unknown, which is no sign of a login. Where nobody keeps
// one it is judged as before logins existed.
func TestADefaultLoginWithNoSignInIsNoFallback(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	if err := os.Remove(filepath.Join(e.adaHome, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	installClaude(t, e.adaHome)
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", workLogin)
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	ada := domain.AccountOf(e.member.ID)
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.owner.ID, ada, "claude", domain.LaunchTUI, ""); err != nil || !ok {
		t.Fatalf("Ada's signed-out default as a fallback where nobody added a login = %v, %v; want it, as before", ok, err)
	}
	e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.owner.ID, ada, "claude", domain.LaunchTUI, ""); err != nil || ok {
		t.Fatalf("Ada's signed-out default as a fallback from Grace's account, which keeps an added login = %v, %v; want refused", ok, err)
	}
	writeHomeFile(t, e.adaHome, ".claude/.credentials.json", workLogin)
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.owner.ID, ada, "claude", domain.LaunchTUI, ""); err != nil || !ok {
		t.Fatalf("Ada's signed-in default as a fallback = %v, %v; want it", ok, err)
	}
}

// A subsession does not inherit its parent's login when that login is
// billed per use, the default login included, or is automatic off and so
// was named for the parent alone: it is chosen for anew.
func TestASubsessionIsChosenForAnewOffAPerUseOrNamedLogin(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	if err := e.db.OrderAgentLogins(ctx, e.member.ID, "claude", []domain.LoginID{work.Login, domain.DefaultLogin}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, domain.DefaultLogin, "claude", domain.LaunchTUI, ""); err != nil || got != work.Login {
		t.Fatalf("a subsession of Ada's session on her API-key default = %q, %v; want work, chosen anew", got, err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", work.Login, false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, work.Login, "claude", domain.LaunchTUI, ""); err != nil || got != "" {
		t.Fatalf("a subsession of Ada's session on work, named and automatic off = %q, %v; want her default, chosen anew", got, err)
	}
}

// Removing a login waits until every kept session container's record was
// read, naming the one that could not be, and a kept container whose
// session cannot be read holds the login: neither is passed over as not on
// it.
func TestLoginInUseFailsClosedOnAnUnreadSession(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	kept := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, AccountMemberID: e.member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.db.CreateRun(ctx, kept); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.UpdateRunStatus(ctx, kept.ID, domain.RunMerged, "closed; retained container", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.sched.sidecarPath(kept.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, e.sched)
	waitFor(t, "recovery", func() bool {
		select {
		case <-e.sched.recoveryReady:
			return true
		default:
			return false
		}
	})
	if inUse, err := e.sched.LoginInUse(ctx, work); err == nil || !strings.Contains(err.Error(), "could not read the record of a kept session container") || !strings.Contains(err.Error(), e.sched.sidecarPath(kept.ID)) {
		t.Fatalf("LoginInUse with an unreadable kept session = %v, %v; want refused, naming the record", inUse, err)
	}
	if err := os.Remove(e.sched.sidecarPath(kept.ID)); err != nil {
		t.Fatal(err)
	}
	// A record whose session row is gone, and that cannot be read, is named
	// too.
	if err := os.WriteFile(e.sched.sidecarPath("run-orphan"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.sched.cleanupTerminalSidecars(ctx)
	if inUse, err := e.sched.LoginInUse(ctx, work); err == nil || !strings.Contains(err.Error(), e.sched.sidecarPath("run-orphan")) {
		t.Fatalf("LoginInUse with an unreadable orphan record = %v, %v; want refused, naming the record", inUse, err)
	}
	if err := os.Remove(e.sched.sidecarPath("run-orphan")); err != nil {
		t.Fatal(err)
	}
	e.sched.cleanupTerminalSidecars(ctx)
	if inUse, err := e.sched.LoginInUse(ctx, work); err != nil || len(inUse) != 0 {
		t.Fatalf("LoginInUse once every record was read = %v, %v; want none", inUse, err)
	}
	e.sched.mu.Lock()
	e.sched.runs["run-gone"] = &supervised{runID: "run-gone", containerID: "container-gone", done: make(chan struct{})}
	e.sched.mu.Unlock()
	t.Cleanup(func() {
		e.sched.mu.Lock()
		delete(e.sched.runs, "run-gone")
		e.sched.mu.Unlock()
	})
	if inUse, err := e.sched.LoginInUse(ctx, work); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("LoginInUse with a kept container whose session cannot be read = %v, %v; want refused", inUse, err)
	}
}

// An agent's refusal for want of a sign-in in a session on an added login
// is remembered for that login, not the default one, and a turn that works
// there forgets it. Its advice names the login and how to add it again.
func TestARefusalOnAnAddedLoginIsKeyedByThatLogin(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, AccountMemberID: e.member.ID, AccountLogin: work.Login, Task: "t", Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunRunning}
	if err := e.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	login := e.sched.runLogin(ctx, run.ID)
	if !login.known {
		t.Fatal("the session's login is unknown")
	}
	advice := e.sched.loginRefusedAdvice(login, acp.NewAuthRequired(nil), refusedUnrestarted)
	if !strings.Contains(advice, "Claude Code login work may be signed out") || !strings.Contains(advice, "cloudfield agent logins remove work, then cloudfield agent login claude --as work") ||
		strings.Contains(advice, "Agents page") {
		t.Fatalf("advice = %q; want it to name work and how to add it again, not the default login's sign-in", advice)
	}
	profile, _ := harness.Lookup("claude")
	if problem, _, err := e.sched.LoginProblem(e.member.ID, e.member.ID, "claude", profile); err != nil || problem != "" {
		t.Fatalf("Ada's default login after a refusal on work = %q, %v; want no problem", problem, err)
	}
	refusedOn := func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		_, ok := e.sched.loginRefusals[loginKey{work, "claude"}]
		return ok
	}
	if !refusedOn() {
		t.Fatalf("refusals = %v; want one on work", e.sched.loginRefusals)
	}
	e.sched.turnWorked(ctx, run.ID)
	if refusedOn() {
		t.Fatal("a turn that worked on work left its refusal")
	}
}

// The configuration copy of a session on an added login is mounted through
// a volume of its own, which removing the container leaves: it goes with
// the copy once the session's container does.
func TestAnAddedLoginSessionsConfigVolumeGoesWithItsContainer(t *testing.T) {
	e := newLoginShareEnv(t)
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", workLogin)
	run, err := e.launchOn(t, work, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's container", func() bool { return e.rt.byName(string(run.ID)) != nil })
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
