//go:build sessioneval

package sessioneval

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestACPCheck drives the real Claude Code ACP adapter, as an Enhanced
// session does, through one turn of a staged session and records how it
// reported and whether it replied after its last report.
// CLOUDFIELD_SESSION_EVAL_ACP is the adapter's entry script. The adapter
// loads the caller's own Claude Code settings, which it cannot leave out.
func TestACPCheck(t *testing.T) {
	adapter := os.Getenv("CLOUDFIELD_SESSION_EVAL_ACP")
	if adapter == "" {
		t.Skip("CLOUDFIELD_SESSION_EVAL_ACP unset")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := envOr("CLOUDFIELD_SESSION_EVAL_OUT", filepath.Join(os.TempDir(), "session-eval"))
	label := envOr("CLOUDFIELD_SESSION_EVAL_LABEL", "acp")
	runs, err := strconv.Atoi(envOr("CLOUDFIELD_SESSION_EVAL_RUNS", "3"))
	if err != nil || runs < 1 {
		t.Fatalf("CLOUDFIELD_SESSION_EVAL_RUNS must be a positive number")
	}
	only := strings.Split(envOr("CLOUDFIELD_SESSION_EVAL_ONLY", "small-fix"), ",")
	if err = os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	scenarios, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range only {
		i := slices.IndexFunc(scenarios, func(s Scenario) bool { return s.ID == id })
		if i < 0 {
			t.Fatalf("CLOUDFIELD_SESSION_EVAL_ONLY: unknown scenario %q", id)
		}
		s := scenarios[i]
		for n := 1; n <= runs; n++ {
			name := label + "-" + s.ID + "-" + strconv.Itoa(n)
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				l, err := Stage(ctx, helper, s, ModeEnhanced)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				var config struct {
					Servers map[string]struct {
						Command string            `json:"command"`
						Env     map[string]string `json:"env"`
					} `json:"mcpServers"`
				}
				raw, err := os.ReadFile(l.MCPConfig)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &config); err != nil {
					t.Fatal(err)
				}
				var mcp []acp.McpServer
				for server, c := range config.Servers {
					var env []acp.EnvVariable
					for k, v := range c.Env {
						env = append(env, acp.EnvVariable{Name: k, Value: v})
					}
					mcp = append(mcp, acp.McpServer{Stdio: &acp.McpServerStdio{Name: server, Command: c.Command, Args: []string{}, Env: env}})
				}
				cmd := exec.CommandContext(ctx, "node", adapter)
				cmd.Dir, cmd.Env, cmd.Stderr = l.Repo, append(slices.Clone(l.Env), "CLAUDE_CODE_EXECUTABLE="+claude), os.Stderr
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
				logPath := filepath.Join(out, name+".items.jsonl")
				_ = os.Remove(logPath)
				sess, err := acphost.Start(ctx, stdout, stdin, acphost.Config{
					LogPath: logPath, Cwd: l.Repo, MCPServers: mcp, SystemPrompt: l.Instructions, ClaudeCode: true, AutoAllow: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(l.Prompt + os.Getenv("CLOUDFIELD_SESSION_EVAL_ACP_SUFFIX"))}, false, nil); err != nil {
					t.Fatal(err)
				}
				started := false
				for {
					st := sess.State()
					started = started || st.TurnInFlight
					// Background work is followed by a turn of the agent's own.
					if started && !st.TurnInFlight && !st.Background {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatalf("turn did not end: %+v", st)
					case <-time.After(500 * time.Millisecond):
					}
				}
				time.Sleep(3 * time.Second)
				info := sess.Info()
				_ = sess.Close()
				var outcomes []string
				for _, c := range l.Coord.Calls() {
					if c.Method == protocol.MethodCoordReport && c.Error == "" && !c.Replay {
						var p protocol.CoordReportParams
						_ = json.Unmarshal(c.Params, &p)
						outcomes = append(outcomes, p.Outcome)
					}
				}
				byCommand, _ := l.CommandReports()
				summary, _ := json.Marshal(map[string]any{"run": name, "agent": info.Name + " " + info.Version, "reports": outcomes, "command_report_calls": byCommand, "log": logPath})
				if err = os.WriteFile(filepath.Join(out, name+".acp.json"), summary, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s", summary)
			})
		}
	}
}
