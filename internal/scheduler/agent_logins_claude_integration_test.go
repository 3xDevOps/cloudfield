//go:build integration

package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// mockMessages is a stand-in for Anthropic's Messages API that records the
// credential each request carries.
type mockMessages struct {
	mu   sync.Mutex
	auth []string
}

func (m *mockMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.auth = append(m.auth, r.Header.Get("Authorization")+r.Header.Get("X-Api-Key"))
	m.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/v1/messages") || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	message := map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-test",
		"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
	if !strings.Contains(string(body), `"stream":true`) {
		message["content"] = []any{map[string]string{"type": "text", "text": "hello"}}
		message["stop_reason"] = "end_turn"
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range []struct {
		name string
		data any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": message}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}},
		{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "hello"}}},
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 1}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	} {
		data, _ := json.Marshal(ev.data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, data)
	}
}

func (m *mockMessages) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.auth...)
}

// copyInto copies src, a file or a directory, to dst, keeping file modes.
func copyInto(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// containerView lays out below root what the container of plan sees in its
// home: the member's home, then each mount below it, in order, over it. It
// returns the mounts laid out.
func containerView(t *testing.T, plan *EnvironmentPlan, root string) []runtime.Mount {
	t.Helper()
	var laid []runtime.Mount
	for _, m := range plan.Mounts {
		if m.ContainerPath != plan.Home && !strings.HasPrefix(m.ContainerPath, plan.Home+"/") {
			continue
		}
		copyInto(t, filepath.Join(m.HostPath, filepath.FromSlash(m.Subpath)), filepath.Join(root, m.ContainerPath))
		laid = append(laid, m)
	}
	return laid
}

// proxyLog records where a request that bypassed the stand-in API tried to
// go, and sends it nowhere.
type proxyLog struct {
	mu    sync.Mutex
	hosts []string
}

func (p *proxyLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.hosts = append(p.hosts, r.Method+" "+r.Host)
	p.mu.Unlock()
	http.Error(w, "no network in this test", http.StatusForbidden)
}

// mountScript bind-mounts each path it is given over itself, read-only
// where asked, in a mount namespace of its own, checks that a read-only one
// cannot be written, then runs the rest of its arguments. Over a mount, as
// in a container, a write fails with EROFS and a rename with EBUSY.
const mountScript = `while [ "$1" != -- ]; do
	mount --bind "$2" "$2" || exit 96
	if [ "$1" = ro ]; then
		mount -o remount,bind,ro "$2" || exit 96
		if [ -w "$2" ]; then echo "$2 is writable" >&2; exit 97; fi
	fi
	shift 2
done
shift
exec "$@"`

// runRealClaude runs claude in the container view below root, each laid out
// mount bind-mounted as the container mounts it, and returns its output and
// exit error.
func runRealClaude(t *testing.T, claude string, plan *EnvironmentPlan, laid []runtime.Mount, root, base, proxy string) (string, error) {
	t.Helper()
	remap := func(p string) string {
		if p == "" {
			return ""
		}
		return filepath.Join(root, p)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + remap(plan.Home),
		"ANTHROPIC_BASE_URL=" + base,
		"HTTPS_PROXY=" + proxy,
		"HTTP_PROXY=" + proxy,
		"NO_PROXY=" + strings.TrimPrefix(base, "http://"),
		"IS_SANDBOX=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		// An image's ENV naming a profile the login does not hold; the
		// plan's own value comes after it, as a container's does.
		"ANTHROPIC_PROFILE=elsewhere",
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR"} {
		if v, ok := plan.Env[key]; ok {
			env = append(env, key+"="+remap(v))
		}
	}
	// The variables the plan sets empty, so an image's ENV cannot fill
	// them; the stand-in's address stays.
	for key, v := range plan.Env {
		if v == "" && key != "ANTHROPIC_BASE_URL" {
			env = append(env, key+"=")
		}
	}
	args := []string{"-rm", "sh", "-c", mountScript, "sh"}
	for _, m := range laid {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		args = append(args, mode, filepath.Join(root, m.ContainerPath))
	}
	args = append(args, "--", claude, "-p", "say hi", "--output-format", "json")
	cmd := exec.Command("unshare", args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	timer := time.AfterFunc(90*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && (exit.ExitCode() == 96 || exit.ExitCode() == 97) {
		t.Fatalf("lay out the mounts: %s", out)
	}
	return string(out), err
}

// defaultLogins are the kinds of login a member's home can hold, written
// into home with made-up values.
var defaultLogins = map[string]func(t *testing.T, home string){
	"subscription": func(t *testing.T, home string) {
		writeHomeFile(t, home, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"test-access-default","refreshToken":"test-refresh-default","expiresAt":4102444800000,"scopes":["user:inference"]}}`)
		writeHomeFile(t, home, ".claude.json", `{"hasCompletedOnboarding":true}`)
	},
	"api key": func(t *testing.T, home string) {
		writeHomeFile(t, home, ".claude.json", `{"hasCompletedOnboarding":true,"primaryApiKey":"test-key-default-not-a-real-key-0000000000"}`)
	},
	"legacy api key": func(t *testing.T, home string) {
		writeHomeFile(t, home, ".claude/.config.json", `{"hasCompletedOnboarding":true,"primaryApiKey":"test-key-legacy-not-a-real-key-000000000"}`)
	},
	"console": func(t *testing.T, home string) {
		writeHomeFile(t, home, ".config/anthropic/configs/default.json", `{"authentication":{"type":"user_oauth"}}`)
		writeHomeFile(t, home, ".config/anthropic/credentials/default.json", `{"access_token":"test-console-default","refresh_token":"test-console-refresh-default","expires_at":4102444800}`)
		writeHomeFile(t, home, ".claude.json", `{"hasCompletedOnboarding":true}`)
	},
}

// TestIntegrationRealClaudeCodeUsesTheAddedLogin runs the unmodified Claude
// Code against a local stand-in for Anthropic's API in the container view
// of every layout a session on an added login can have: each kind of added
// login over each kind of default login, in the owner's own session and in
// a teammate's, each mount bind-mounted and read-only where the session's
// is. Claude Code must succeed and every request must carry the added
// login's credential. It needs Claude Code on PATH and unprivileged user
// and mount namespaces; no real login is read or used, and a request to
// anywhere but the stand-in goes to a proxy that refuses it.
func TestIntegrationRealClaudeCodeUsesTheAddedLogin(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("Claude Code is not on PATH")
	}
	profile, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("no claude profile")
	}
	added := map[string]string{
		"subscription": `{"claudeAiOauth":{"accessToken":"test-access-added","refreshToken":"test-refresh-added","expiresAt":4102444800000,"scopes":["user:inference"]}}`,
		"console":      "",
	}
	wantAuth := map[string]string{"subscription": "Bearer test-access-added", "console": "Bearer test-console-added"}
	for defaultKind, writeDefault := range defaultLogins {
		for addedKind, file := range added {
			for _, teammate := range []bool{false, true} {
				name := fmt.Sprintf("%s over %s", addedKind, defaultKind)
				if teammate {
					name += " in a teammate's session"
				}
				t.Run(name, func(t *testing.T) {
					e := newLoginShareEnv(t)
					if err := os.Remove(filepath.Join(e.adaHome, ".claude.json")); err != nil {
						t.Fatal(err)
					}
					writeDefault(t, e.adaHome)
					owner := e.member.ID
					if teammate {
						owner = e.owner.ID
						if _, err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
							t.Fatal(err)
						}
					}
					login := e.addLogin(t, owner, "lgn-added00000", "added", file)
					if addedKind == "console" {
						dir, _ := e.logins.Path(owner, "claude", login.Login)
						writeHomeFile(t, dir, "anthropic/credentials/default.json", `{"access_token":"test-console-added","refresh_token":"test-console-refresh-added","expires_at":4102444800}`)
					}
					member, err := e.db.GetMember(t.Context(), e.member.ID)
					if err != nil {
						t.Fatal(err)
					}
					ws, err := e.db.GetWorkspace(t.Context(), e.ws.ID)
					if err != nil {
						t.Fatal(err)
					}
					run := &domain.Run{ID: "run-gate", MemberID: member.ID, HomeMemberID: member.ID, AccountMemberID: owner, AccountLogin: login.Login, Harness: "claude"}
					plan, err := e.sched.BuildEnvironmentPlan(t.Context(), run, ws, member, profile, EnvironmentPurposeRun)
					if err != nil {
						t.Fatal(err)
					}
					// Another session in the home writes a legacy configuration
					// holding a key once this one has started.
					writeHomeFile(t, e.adaHome, ".claude/.config.json", `{"hasCompletedOnboarding":true,"primaryApiKey":"test-key-planted-not-a-real-key-00000000"}`)
					mock := &mockMessages{}
					server := httptest.NewServer(mock)
					defer server.Close()
					proxy := &proxyLog{}
					proxyServer := httptest.NewServer(proxy)
					defer proxyServer.Close()
					root := t.TempDir()
					laid := containerView(t, plan, root)
					var readOnly []string
					for _, m := range laid {
						if m.ReadOnly {
							readOnly = append(readOnly, strings.TrimPrefix(m.ContainerPath, plan.Home+"/"))
						}
					}
					out, runErr := runRealClaude(t, claude, plan, laid, root, server.URL, proxyServer.URL)
					t.Logf("read-only: %v; requests elsewhere: %v", readOnly, proxy.hosts)
					if runErr != nil {
						t.Errorf("claude: %v: %.600s", runErr, out)
					}
					seen := mock.seen()
					if len(seen) == 0 {
						t.Fatal("Claude Code sent no request")
					}
					for _, auth := range seen {
						if auth != wantAuth[addedKind] {
							t.Fatalf("a request carried %q; want every one to carry %q", auth, wantAuth[addedKind])
						}
					}
				})
			}
		}
	}
}

// TestIntegrationRealClaudeCodePrefersHomeSettingsToAnAddedLogin shows,
// with the unmodified Claude Code against the stand-in API, that an
// apiKeyHelper or a key under env in ~/.claude/settings.json of the
// session's home pays instead of a mounted added login of either kind, and
// that a plan for such a session is refused.
func TestIntegrationRealClaudeCodePrefersHomeSettingsToAnAddedLogin(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("Claude Code is not on PATH")
	}
	profile, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("no claude profile")
	}
	settings := map[string]struct{ file, auth string }{
		"apiKeyHelper":                {`{"apiKeyHelper":"echo test-helper-key"}`, "Bearer test-helper-keytest-helper-key"},
		"ANTHROPIC_API_KEY under env": {`{"env":{"ANTHROPIC_API_KEY":"test-settings-key"}}`, "test-settings-key"},
	}
	added := map[string]string{
		"subscription": `{"claudeAiOauth":{"accessToken":"test-access-added","refreshToken":"test-refresh-added","expiresAt":4102444800000,"scopes":["user:inference"]}}`,
		"console":      "",
	}
	for setting, want := range settings {
		for addedKind, file := range added {
			t.Run(setting+" over "+addedKind, func(t *testing.T) {
				e := newLoginShareEnv(t)
				login := e.addLogin(t, e.member.ID, "lgn-added00000", "added", file)
				member, err := e.db.GetMember(t.Context(), e.member.ID)
				if err != nil {
					t.Fatal(err)
				}
				ws, err := e.db.GetWorkspace(t.Context(), e.ws.ID)
				if err != nil {
					t.Fatal(err)
				}
				run := &domain.Run{ID: "run-gate", MemberID: member.ID, HomeMemberID: member.ID, AccountMemberID: member.ID, AccountLogin: login.Login, Harness: "claude"}
				plan, err := e.sched.BuildEnvironmentPlan(t.Context(), run, ws, member, profile, EnvironmentPurposeRun)
				if err != nil {
					t.Fatal(err)
				}
				writeHomeFile(t, e.adaHome, ".claude/settings.json", want.file)
				mock := &mockMessages{}
				server := httptest.NewServer(mock)
				defer server.Close()
				proxy := &proxyLog{}
				proxyServer := httptest.NewServer(proxy)
				defer proxyServer.Close()
				root := t.TempDir()
				out, runErr := runRealClaude(t, claude, plan, containerView(t, plan, root), root, server.URL, proxyServer.URL)
				if runErr != nil {
					t.Errorf("claude: %v: %.600s", runErr, out)
				}
				seen := mock.seen()
				if len(seen) == 0 {
					t.Fatal("Claude Code sent no request")
				}
				for _, auth := range seen {
					if auth != want.auth {
						t.Fatalf("a request carried %q; want the setting's %q", auth, want.auth)
					}
				}
				if _, err = e.sched.BuildEnvironmentPlan(t.Context(), run, ws, member, profile, EnvironmentPurposeRun); !errors.Is(err, ErrEnvironmentPays) {
					t.Fatalf("a plan with %s in the home's settings: %v; want refused", setting, err)
				}
			})
		}
	}
}
