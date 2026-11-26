package memberhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHomeFile(t *testing.T, home, rel, body string) {
	t.Helper()
	name := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ownerLogin(t *testing.T, manager *Manager) *OwnerLogin {
	t.Helper()
	login, err := manager.OwnerLogin("owner", "claude")
	if err != nil {
		t.Fatal(err)
	}
	return login
}

// Claude Code keeps an Anthropic Console sign-in outside its login file:
// the API key in its configuration file, or an Anthropic profile.
func TestOwnerLoginFindsAConsoleSignIn(t *testing.T) {
	const profile = `{"authentication":{"type":"user_oauth"}}`
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no sign-in":             {files: map[string]string{".claude.json": `{"hasCompletedOnboarding":true}`}},
		"only the login file":    {},
		"API key":                {files: map[string]string{".claude.json": `{"primaryApiKey":"fixture-key"}`}, want: ".claude.json"},
		"blank API key":          {files: map[string]string{".claude.json": `{"primaryApiKey":" "}`}},
		"legacy file is read":    {files: map[string]string{".claude/.config.json": `{"primaryApiKey":"fixture-key"}`}, want: ".claude/.config.json"},
		"legacy file hides it":   {files: map[string]string{".claude/.config.json": `{"numStartups":1}`, ".claude.json": `{"primaryApiKey":"fixture-key"}`}},
		"empty legacy file":      {files: map[string]string{".claude/.config.json": "", ".claude.json": `{"primaryApiKey":"fixture-key"}`}, want: ".claude.json"},
		"default profile":        {files: map[string]string{".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`}, want: ".config/anthropic"},
		"active profile":         {files: map[string]string{".config/anthropic/active_config": "work\n", ".config/anthropic/configs/work.json": profile, ".config/anthropic/credentials/work.json": `{"refresh_token":"fixture-refresh"}`}, want: ".config/anthropic"},
		"inactive profile":       {files: map[string]string{".config/anthropic/active_config": "work\n", ".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`}},
		"profile without login":  {files: map[string]string{".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{}`}},
		"federation profile":     {files: map[string]string{".config/anthropic/configs/default.json": `{"authentication":{"type":"oidc_federation"}}`, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`}},
		"profile name is a path": {files: map[string]string{".config/anthropic/active_config": "sub/work", ".config/anthropic/configs/sub/work.json": profile, ".config/anthropic/credentials/sub/work.json": `{"access_token":"fixture-access"}`}},
		"expired profile":        {files: map[string]string{".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access","expires_at":1}`}},
		"profile that renews":    {files: map[string]string{".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":1}`}, want: ".config/anthropic"},
		"profile beside a configuration file it cannot read": {files: map[string]string{".claude.json": `not json`, ".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`}, want: ".config/anthropic"},
		"profile name too long":                              {files: map[string]string{".config/anthropic/active_config": strings.Repeat("n", 251), ".config/anthropic/configs/default.json": profile, ".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			manager, home, _ := newLoginHomes(t)
			for rel, body := range tc.files {
				writeHomeFile(t, home, rel, body)
			}
			login := ownerLogin(t, manager)
			switch {
			case tc.want == "" && login != nil:
				t.Fatalf("OwnerLogin = %+v, want none", login)
			case tc.want != "" && (login == nil || login.File != tc.want || login.Stamp == ""):
				t.Fatalf("OwnerLogin = %+v, want the login in %s", login, tc.want)
			}
			if other, err := manager.OwnerLogin("other", "claude"); err != nil || other != nil {
				t.Fatalf("another member's OwnerLogin = %+v, %v, want none", other, err)
			}
			if codex, err := manager.OwnerLogin("owner", "codex"); err != nil || codex != nil {
				t.Fatalf("codex OwnerLogin = %+v, %v, want none", codex, err)
			}
		})
	}
}

// Claude Code rewrites its configuration file on every start, so the stamp
// follows the key and not the file.
func TestOwnerLoginStampFollowsTheLogin(t *testing.T) {
	manager, home, _ := newLoginHomes(t)
	writeHomeFile(t, home, ".claude.json", `{"primaryApiKey":"fixture-key","numStartups":1}`)
	first := ownerLogin(t, manager).Stamp
	writeHomeFile(t, home, ".claude.json", `{"primaryApiKey":"fixture-key","numStartups":2}`)
	if again := ownerLogin(t, manager).Stamp; again != first {
		t.Fatalf("stamp after the agent rewrote its file = %q, want %q", again, first)
	}
	writeHomeFile(t, home, ".claude.json", `{"primaryApiKey":"fixture-other-key","numStartups":2}`)
	if changed := ownerLogin(t, manager).Stamp; changed == first {
		t.Fatal("stamp did not change with the key")
	}
}

func TestOwnerLoginRefusesAFileItCannotTrust(t *testing.T) {
	manager, home, other := newLoginHomes(t)
	writeHomeFile(t, other, ".claude.json", `{"primaryApiKey":"fixture-key"}`)
	if err := os.Symlink(filepath.Join(other, ".claude.json"), filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if login, err := manager.OwnerLogin("owner", "claude"); err == nil {
		t.Fatalf("OwnerLogin through a symlink = %+v, want an error", login)
	}
	if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	// A login read before stays refused once its file has another link.
	writeHomeFile(t, home, ".claude.json", `{"primaryApiKey":"fixture-key"}`)
	if login, err := manager.OwnerLogin("owner", "claude"); err != nil || login == nil {
		t.Fatalf("OwnerLogin = %+v, %v, want the login", login, err)
	}
	if err := os.Link(filepath.Join(home, ".claude.json"), filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	if login, err := manager.OwnerLogin("owner", "claude"); err == nil {
		t.Fatalf("OwnerLogin of a file with another hard link = %+v, want an error", login)
	}
	for _, name := range []string{".claude.json", "linked"} {
		if err := os.Remove(filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeHomeFile(t, home, ".claude.json", `not json`)
	if login, err := manager.OwnerLogin("owner", "claude"); err == nil {
		t.Fatalf("OwnerLogin of a malformed file = %+v, want an error", login)
	}
	if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, ".config/anthropic/active_config", strings.Repeat("n", 4096))
	if login, err := manager.OwnerLogin("owner", "claude"); err == nil || len(err.Error()) > 512 {
		t.Fatalf("OwnerLogin of an oversized active_config = %+v, %v, want a short error", login, err)
	}
	if login, err := manager.OwnerLogin("nobody", "claude"); err != nil || login != nil {
		t.Fatalf("OwnerLogin without a home = %+v, %v, want none", login, err)
	}
}

// A dotfiles setup symlinks ~/.claude or ~/.config. The API key in
// ~/.claude.json is on neither path and still counts; a profile behind the
// symlink is not read and is no error.
func TestOwnerLoginBesideSymlinkedDotDirectories(t *testing.T) {
	manager, home, _ := newLoginHomes(t)
	for _, dir := range []string{".claude", ".config"} {
		if err := os.RemoveAll(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, "dotfiles", dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("dotfiles", dir), filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	writeHomeFile(t, home, "dotfiles/.config/anthropic/configs/default.json", `{"authentication":{"type":"user_oauth"}}`)
	writeHomeFile(t, home, "dotfiles/.config/anthropic/credentials/default.json", `{"access_token":"fixture-access"}`)
	if login, err := manager.OwnerLogin("owner", "claude"); err != nil || login != nil {
		t.Fatalf("OwnerLogin behind a symlinked ~/.config = %+v, %v, want none and no error", login, err)
	}
	writeHomeFile(t, home, ".claude.json", `{"primaryApiKey":"fixture-key"}`)
	for range 2 {
		if login, err := manager.OwnerLogin("owner", "claude"); err != nil || login == nil || login.File != ".claude.json" {
			t.Fatalf("OwnerLogin beside a symlinked ~/.claude = %+v, %v, want the key in .claude.json", login, err)
		}
	}
	if login, err := manager.OwnerLogin("nobody", "claude"); err != nil || login != nil {
		t.Fatalf("OwnerLogin without a home = %+v, %v, want none", login, err)
	}
}
