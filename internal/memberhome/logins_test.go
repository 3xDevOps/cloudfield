package memberhome

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func newTestLogins(t *testing.T) *Logins {
	t.Helper()
	l, err := NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func writeLoginFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	subscriptionLogin = `{"claudeAiOauth":{"accessToken":"test-access-one","refreshToken":"test-refresh-one","expiresAt":4102444800000,"subscriptionType":"max"}}`
	cachedAccount     = `{"oauthAccount":{"emailAddress":"ada@example.test","organizationUuid":"org-test-one","organizationName":"Ada's Org"}}`
)

// A login's directory is owner-only, holds its own files, and two logins of
// one member never share one.
func TestLoginDirectoriesAreOwnerOnlyAndSeparate(t *testing.T) {
	l := newTestLogins(t)
	one, err := l.Add("mem-ada", "claude", "lgn-one0000000")
	if err != nil {
		t.Fatal(err)
	}
	two, err := l.Add("mem-ada", "claude", "lgn-two0000000")
	if err != nil {
		t.Fatal(err)
	}
	if one == two || filepath.Dir(one) != filepath.Dir(two) {
		t.Fatalf("login directories %s and %s; want two beside each other", one, two)
	}
	for _, check := range []struct {
		path string
		mode fs.FileMode
	}{
		{one, 0o700}, {filepath.Join(one, LoginAnthropic), 0o700}, {filepath.Join(one, LoginCredentials), 0o600},
		{filepath.Join(l.Root(), "mem-ada"), 0o700}, {filepath.Join(l.Root(), "mem-ada", "claude"), 0o700},
	} {
		info, err := os.Stat(check.path)
		if err != nil || info.Mode().Perm() != check.mode {
			t.Fatalf("%s: %v, %v; want mode %o", check.path, info, err, check.mode)
		}
	}
	a, _ := os.Stat(filepath.Join(one, LoginCredentials))
	b, _ := os.Stat(filepath.Join(two, LoginCredentials))
	if os.SameFile(a, b) {
		t.Fatal("two logins share one credentials file")
	}
	if _, err := l.Add("mem-ada", "claude", "lgn-one0000000"); err == nil {
		t.Fatal("creating a login over an existing one succeeded")
	}
	for _, bad := range []struct {
		member domain.MemberID
		agent  string
		id     domain.LoginID
	}{{"mem-ada", "claude", domain.DefaultLogin}, {"mem-ada", "claude", ""}, {"mem-ada", "../x", "lgn-x"}, {"../mem", "claude", "lgn-x"}, {"mem-ada", "claude", "lgn-../x"}} {
		if _, err := l.Path(bad.member, bad.agent, bad.id); err == nil {
			t.Errorf("Path(%q, %q, %q) succeeded", bad.member, bad.agent, bad.id)
		}
	}
}

// Inspect tells what a sign-in left: a subscription, a keyless Console
// sign-in, a Console API key, or nothing.
func TestInspectReadsWhatTheSignInSaved(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  LoginIdentity
	}{
		{"nothing", nil, LoginIdentity{}},
		{"subscription", map[string]string{LoginCredentials: subscriptionLogin, LoginConfig: cachedAccount},
			LoginIdentity{Kind: LoginSubscription, Plan: "max", Email: "ada@example.test", OrganizationID: "org-test-one", OrganizationName: "Ada's Org"}},
		{"console", map[string]string{
			"anthropic/configs/default.json":     `{"authentication":{"type":"user_oauth"}}`,
			"anthropic/credentials/default.json": `{"access_token":"test-console-access","refresh_token":"test-console-refresh"}`,
			LoginConfig:                          cachedAccount,
		}, LoginIdentity{Kind: LoginConsole, Email: "ada@example.test", OrganizationID: "org-test-one", OrganizationName: "Ada's Org"}},
		{"api key", map[string]string{LoginConfig: `{"primaryApiKey":"test-not-a-key"}`}, LoginIdentity{Kind: LoginAPIKey}},
		{"federation profile", map[string]string{"anthropic/configs/default.json": `{"authentication":{"type":"oidc_federation"}}`}, LoginIdentity{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newTestLogins(t)
			dir, err := l.Add("mem-ada", "claude", "lgn-one0000000")
			if err != nil {
				t.Fatal(err)
			}
			for rel, content := range tc.files {
				writeLoginFile(t, dir, rel, content)
			}
			got, err := l.Inspect("mem-ada", "claude", "lgn-one0000000")
			if err != nil || got != tc.want {
				t.Fatalf("Inspect = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// Settle keeps what identifies the login and drops the sign-in's scratch,
// so the configuration copy that held an account and any backups of it go.
func TestSettleKeepsTheIdentityAndDropsTheScratch(t *testing.T) {
	l := newTestLogins(t)
	dir, err := l.Add("mem-ada", "claude", "lgn-one0000000")
	if err != nil {
		t.Fatal(err)
	}
	writeLoginFile(t, dir, LoginCredentials, subscriptionLogin)
	writeLoginFile(t, dir, LoginConfig, cachedAccount)
	writeLoginFile(t, dir, "backups/.claude.json.backup.1", cachedAccount)
	writeLoginFile(t, dir, loginLegacyMask, "")
	identity, err := l.Inspect("mem-ada", "claude", "lgn-one0000000")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Settle("mem-ada", "claude", "lgn-one0000000", identity); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{LoginConfig, LoginBackups, loginLegacyMask} {
		if _, statErr := os.Lstat(filepath.Join(dir, gone)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s after Settle: %v; want it gone", gone, statErr)
		}
	}
	if got, idErr := l.Identity("mem-ada", "claude", "lgn-one0000000"); idErr != nil || got != identity {
		t.Fatalf("Identity = %+v, %v; want %+v", got, idErr, identity)
	}
	data, err := l.ReadCredential("mem-ada", "claude", "lgn-one0000000", 1<<20)
	if err != nil || string(data) != subscriptionLogin {
		t.Fatalf("ReadCredential = %q, %v; want the login file", data, err)
	}
}

// A login file planted as a symlink is refused, not followed.
func TestLoginReadsRefuseASymlink(t *testing.T) {
	l := newTestLogins(t)
	dir, err := l.Add("mem-ada", "claude", "lgn-one0000000")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	writeLoginFile(t, filepath.Dir(outside), filepath.Base(outside), subscriptionLogin)
	if err := os.Remove(filepath.Join(dir, LoginCredentials)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, LoginCredentials)); err != nil {
		t.Fatal(err)
	}
	if data, err := l.ReadCredential("mem-ada", "claude", "lgn-one0000000", 1<<20); err == nil {
		t.Fatalf("ReadCredential through a symlink = %q; want a refusal", data)
	}
	if got, err := l.Inspect("mem-ada", "claude", "lgn-one0000000"); err == nil {
		t.Fatalf("Inspect through a symlink = %+v; want a refusal", got)
	}
}

// A malformed login file's error names the file and never quotes it.
func TestLoginParseErrorsQuoteNothing(t *testing.T) {
	l := newTestLogins(t)
	dir, err := l.Add("mem-ada", "claude", "lgn-one0000000")
	if err != nil {
		t.Fatal(err)
	}
	writeLoginFile(t, dir, LoginCredentials, `{"claudeAiOauth":{"accessToken":"test-access-one"`)
	_, err = l.Inspect("mem-ada", "claude", "lgn-one0000000")
	if err == nil || containsAny(err.Error(), "test-access-one", "claudeAiOauth") {
		t.Fatalf("Inspect of a torn file: %v; want an error that quotes nothing", err)
	}
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

// List and Move see each login directory, and Move gives one to another
// member as a merge does.
func TestLoginsListAndMove(t *testing.T) {
	l := newTestLogins(t)
	for _, id := range []domain.LoginID{"lgn-one0000000", "lgn-two0000000"} {
		if _, err := l.Add("mem-bo", "claude", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Move("mem-bo", "mem-ada", "claude", "lgn-two0000000"); err != nil {
		t.Fatal(err)
	}
	ada, err := l.List("mem-ada")
	if err != nil || len(ada) != 1 || ada[0].ID != "lgn-two0000000" {
		t.Fatalf("Ada's logins = %+v, %v; want the moved one", ada, err)
	}
	all, err := l.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("every login = %+v, %v; want two", all, err)
	}
	if err := l.RemoveMember("mem-bo"); err != nil {
		t.Fatal(err)
	}
	if bo, err := l.List("mem-bo"); err != nil || len(bo) != 0 {
		t.Fatalf("Bo's logins after removal = %+v, %v; want none", bo, err)
	}
}
