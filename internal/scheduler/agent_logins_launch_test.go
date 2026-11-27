package scheduler

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/permissions"
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
// no key.
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
		variable   bool
		want       error
		wantText   string
	}{
		{name: "off for shares", file: workLogin, unshared: true, want: permissions.ErrDenied},
		{name: "signed out", file: `{"claudeAiOauth":{"accessToken":"test-access-old","expiresAt":1}}`, want: ErrSignedOut},
		{name: "isolated Console", own: true, permission: domain.PermissionIsolated, wantText: "Isolated session cannot use a Console login"},
		{name: "a workspace key pays", own: true, file: workLogin, variable: true, wantText: "would not pay"},
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
			if tc.variable {
				if err := e.db.SetWorkspaceEnvironment(t.Context(), e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}}); err != nil {
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
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != work.Login {
		t.Fatalf("Ada's start on Grace's account = %q, %v; want work, the first shared", got, err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
		t.Fatalf("Grace's own start = %q, %v; want her default", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, work.Login, "claude", domain.LaunchTUI); err != nil || got != work.Login {
		t.Fatalf("a subsession of a session on work = %q, %v; want work inherited", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, console.Login, "claude", domain.LaunchTUI); err != nil || got != "" {
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
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchTUI); err != nil || got != work.Login {
		t.Fatalf("Ada's subsession naming Grace's account = %q, %v; want work", got, err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, domain.DefaultLogin, "claude", domain.LaunchTUI); err != nil || got != work.Login {
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
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI); !errors.Is(err, ErrNoStartLogin) {
		t.Fatalf("Grace's start with no automatic login = %q, %v; want refused", got, err)
	}
	if _, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("Ada's start with no shared login: %v; want denied", err)
	}
	if got, err := e.sched.ChildLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, work.Login, "claude", domain.LaunchTUI); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("a subsession of Ada's on Grace's unshared login = %q, %v; want denied", got, err)
	}
	// A workspace variable that signs Claude Code in pays: no login is
	// chosen.
	if err := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, domain.WorkspaceEnvironment{Variables: map[string]string{"ANTHROPIC_API_KEY": "test-not-a-key"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
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
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
		t.Fatalf("Grace's start with no added login and a signed-out default = %q, %v; want the default login, whose launch says why", got, err)
	}
	work := e.addLogin(t, e.member.ID, "lgn-work000000", "work", expired)
	if err := e.db.OrderAgentLogins(ctx, e.member.ID, "claude", []domain.LoginID{work.Login, domain.DefaultLogin}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI)
	if !errors.Is(err, ErrNoStartLogin) || !strings.Contains(err.Error(), ": work: ") || !strings.Contains(err.Error(), "default is automatic off; name one with cloudfield session --login <name>") {
		t.Fatalf("Ada's start with work signed out and her API-key default automatic off: %v; want refused naming both", err)
	}
	dir, _ := e.logins.Path(e.member.ID, "claude", work.Login)
	writeHomeFile(t, dir, memberhome.LoginCredentials, workLogin)
	if err = e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", work.Login, false); err != nil {
		t.Fatal(err)
	}
	_, err = e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI)
	if !errors.Is(err, ErrNoStartLogin) || !strings.Contains(err.Error(), "work is automatic off; default is automatic off") {
		t.Fatalf("Ada's start with every login automatic off: %v; want refused naming both", err)
	}
	if err := e.db.SetAgentLoginAutomatic(ctx, e.member.ID, "claude", domain.DefaultLogin, true); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.member.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
		t.Fatalf("Ada's start with her default automatic again = %q, %v; want the default login", got, err)
	}
}

// A teammate who picks an account whose owner added logins may still run
// the owner's Claude Code installation, as before logins existed; only a
// fallback the launcher did not pick refuses to borrow it.
func TestATeammatesStartOnASharedAccountMayBorrowTheOwnersInstallation(t *testing.T) {
	e := newLoginShareEnv(t)
	ctx := t.Context()
	writeHomeFile(t, e.ownerHome, ".claude/.credentials.json", workLogin)
	installClaude(t, e.ownerHome)
	if _, err := e.db.ShareAccount(ctx, e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
		t.Fatalf("Ada's start before Grace adds a login = %q, %v; want the default login", got, err)
	}
	work := e.addLogin(t, e.owner.ID, "lgn-work000000", "work", workLogin)
	if got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI); err != nil || got != "" {
		t.Fatalf("Ada's start after Grace adds a login = %q, %v; want the default login, first in Grace's order", got, err)
	}
	if err := e.db.SetAgentLoginShared(ctx, e.owner.ID, "claude", domain.DefaultLogin, false); err != nil {
		t.Fatal(err)
	}
	got, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI)
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
	if ok, err := e.sched.FallbackLaunchable(ctx, e.member.ID, e.member.ID, work, "claude", domain.LaunchTUI); err != nil || ok {
		t.Fatalf("work as a fallback Ada did not pick = %v, %v; want refused, as it borrows Grace's installation", ok, err)
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
	_, err := e.sched.StartLogin(ctx, e.ws.ID, e.member.ID, e.owner.ID, "claude", domain.LaunchTUI)
	if !errors.Is(err, permissions.ErrDenied) || !strings.Contains(err.Error(), "work: ") || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "not shared with you") {
		t.Fatalf("Ada's start on Grace's signed-out logins: %v; want denied, naming work and why", err)
	}
}
