package sessioneval

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/coordcli"
	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/envname"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/mcpbridge"
	"github.com/3xDevOps/cloudfield/internal/shellquote"
)

// The variables that turn a re-executed test binary into the in-container
// command or the MCP bridge, and tell it which socket to use.
const (
	helperEnv = "SESSIONEVAL_HELPER"
	socketEnv = "SESSIONEVAL_SOCKET"

	helperCLI = "cli"
	helperMCP = "mcp"
)

//go:embed all:testdata/fixture
var fixture embed.FS

// A go.mod under testdata would make the fixture a module of its own, which
// cannot be embedded.
const fixtureModule = "module example.com/shop\n\ngo 1.21\n"

// Helper runs the real in-container command or MCP bridge against the
// socket named in the environment and returns the exit status.
func Helper(kind string, args []string) int {
	socket := os.Getenv(socketEnv)
	switch kind {
	case helperCLI:
		code, err := coordcli.Run(context.Background(), args, coordcli.Config{Socket: socket})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return code
	case helperMCP:
		if err := mcpbridge.Run(context.Background(), mcpbridge.Config{Socket: socket}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "sessioneval: unknown %s %q\n", helperEnv, kind)
	return 2
}

// Launch is one staged session: a repository to work in, the fake socket
// behind the in-container command and the MCP bridge, and the environment a
// Standard Claude Code launch sets.
type Launch struct {
	Repo         string
	Prompt       string
	Instructions string
	MCPConfig    string
	Env          []string
	Coord        *Coord
	dir          string
}

func (l *Launch) Close() {
	l.Coord.Close()
	_ = os.RemoveAll(l.dir)
}

// sessionEnv are the variables the scheduler and a native Claude Code launch
// set in a Standard session's container.
func sessionEnv() map[string]string {
	return map[string]string{
		coordtransport.RunIDEnv:        runID,
		"AETHER_WORKSPACE_ID":          workspaceID,
		"AETHER_ACCOUNT_MEMBER_ID":     memberID,
		coordtransport.HarnessEnv:      "claude",
		coordtransport.ManagedHooksEnv: "1",
		coordtransport.WakeEnv:         "1",
		coordtransport.RoleCardEnv:     "1",
	}
}

// claudeLoginEnv are the CLAUDE* variables that say where Claude Code's login
// is.
var claudeLoginEnv = []string{"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"}

// hostEnv is this process's environment without what would leak a
// surrounding session into the run: Cloudfield's own session variables and
// those of a Claude Code session the eval was started from.
func hostEnv() []string {
	drop := make(map[string]bool)
	for _, name := range coordtransport.SessionEnv {
		drop[name], drop[envname.New(name)] = true, true
	}
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if drop[name] || strings.HasPrefix(name, "CLAUDE") && !slices.Contains(claudeLoginEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Stage builds the session of s. helper is the test binary, which Helper
// turns into `aether-internal` on PATH and into the "aether" MCP server.
func Stage(ctx context.Context, helper string, s Scenario) (_ *Launch, err error) {
	dir, err := os.MkdirTemp("", "sessioneval-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	repo, bin := filepath.Join(dir, "repo"), filepath.Join(dir, "bin")
	env := append(hostEnv(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+memberName, "GIT_COMMITTER_NAME="+memberName,
		"GIT_AUTHOR_EMAIL=eval@example.com", "GIT_COMMITTER_EMAIL=eval@example.com",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	profile, _ := harness.Lookup("claude")
	for _, vars := range []map[string]string{profile.Env, sessionEnv()} {
		for _, name := range slices.Sorted(maps.Keys(vars)) {
			env = append(env, name+"="+vars[name])
		}
	}

	files, err := fs.Sub(fixture, "testdata/fixture")
	if err != nil {
		return nil, err
	}
	if err = os.CopyFS(repo, files); err != nil {
		return nil, fmt.Errorf("sessioneval: write the fixture: %w", err)
	}
	if err = os.WriteFile(filepath.Join(repo, "go.mod"), []byte(fixtureModule), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "add the shop helpers"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = repo, env
		if out, gitErr := cmd.CombinedOutput(); gitErr != nil {
			return nil, fmt.Errorf("sessioneval: git %s: %w: %s", args[0], gitErr, out)
		}
	}

	coord, err := NewCoord(dir, Session{RunID: runID, Parent: s.Parent, Task: s.Prompt})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			coord.Close()
		}
	}()
	if err = os.Mkdir(bin, 0o755); err != nil {
		return nil, err
	}
	script := fmt.Sprintf("#!/bin/sh\n%s=%s %s=%s exec %s \"$@\"\n", helperEnv, helperCLI, socketEnv, shellquote.Quote(coord.Path), shellquote.Quote(helper))
	// The real CLI and bridge name whichever command this machine has.
	for _, name := range []string{coordhooks.OldCLI, coordhooks.NewCLI} {
		if err = os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			return nil, err
		}
	}
	bridgeEnv := sessionEnv()
	bridgeEnv[helperEnv], bridgeEnv[socketEnv] = helperMCP, coord.Path
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		mcpbridge.ServerName: map[string]any{"type": "stdio", "command": helper, "args": []string{}, "env": bridgeEnv},
	}})
	if err != nil {
		return nil, err
	}
	mcpConfig := filepath.Join(dir, "mcp.json")
	if err = os.WriteFile(mcpConfig, config, 0o600); err != nil {
		return nil, err
	}
	return &Launch{Repo: repo, Prompt: s.Prompt, Instructions: coordhooks.RoleCard(s.Guide()), MCPConfig: mcpConfig, Env: env, Coord: coord, dir: dir}, nil
}

const startTool = "mcp__" + mcpbridge.ServerName + "__aether_subsession_start"

var (
	startCommand = regexp.MustCompile(`-internal subsession start([^|;&\n]*)`)
	helpFlag     = regexp.MustCompile(`(^|\s)(-h|--help)(\s|$)`)
)

// Reply is what one agent run produced besides its coordination calls.
type Reply struct {
	Answer string
	// Model is the model that wrote most of the output.
	Model string
	// CostUSD is nil when the harness did not report it.
	CostUSD *float64
	// Tools counts the agent's own tool calls by name.
	Tools map[string]int
	// Subagents is how many subagents the harness says the agent started.
	Subagents int
	// StartAttempts counts the agent's tool calls that tried to start a
	// subsession, including ones the CLI refused before the socket.
	StartAttempts int
	// Transcript is the harness's raw output.
	Transcript []byte
}

// Agent runs one staged session until the agent process exits.
type Agent func(ctx context.Context, l *Launch) (Reply, error)

// Claude drives Claude Code with the command line of a Background launch,
// the instructions flag a Standard launch carries, and its own tools on.
// User settings, plugins, hooks, skills and MCP servers of the person running
// the eval are left out; maxUSD stops a run that spends more.
func Claude(model string, maxUSD float64) Agent {
	return func(ctx context.Context, l *Launch) (Reply, error) {
		profile, _ := harness.Lookup("claude")
		argv := harness.Argv(profile.HeadlessArgs, l.Prompt, profile.LaunchArgs(model, ""))
		argv = append(argv, profile.InstructionLaunchArgs(coordtransport.MountDir, l.Instructions)...)
		argv = append(argv, "--mcp-config", l.MCPConfig, "--strict-mcp-config", "--setting-sources", "project",
			"--no-session-persistence", "--max-budget-usd", strconv.FormatFloat(maxUSD, 'f', -1, 64))
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env, cmd.WaitDelay = l.Repo, l.Env, 10*time.Second
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		reply, resulted, err := parseStream(stdout.Bytes())
		reply.Transcript = stdout.Bytes()
		switch {
		case err != nil:
			return reply, err
		case !resulted && runErr != nil:
			return reply, fmt.Errorf("claude: %w: %s", runErr, stderr.String())
		case !resulted:
			return reply, errors.New("claude: the output has no result line")
		}
		return reply, nil
	}
}

// parseStream reads Claude Code's stream-json output: the tool calls of each
// assistant message and the final result line. resulted reports whether that
// line was there.
func parseStream(out []byte) (reply Reply, resulted bool, err error) {
	reply.Tools = make(map[string]int)
	for line := range bytes.Lines(out) {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Result  string   `json:"result"`
			Subtype string   `json:"subtype"`
			IsError bool     `json:"is_error"`
			Cost    *float64 `json:"total_cost_usd"`
			Usage   map[string]struct {
				Out int `json:"outputTokens"`
			} `json:"modelUsage"`
			Subagents struct {
				Spawned int `json:"spawned"`
			} `json:"subagent_stats"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "assistant":
			var blocks []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					Command string `json:"command"`
				} `json:"input"`
			}
			_ = json.Unmarshal(msg.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				reply.Tools[b.Name]++
				if b.Name == startTool {
					reply.StartAttempts++
				}
				for _, m := range startCommand.FindAllStringSubmatch(b.Input.Command, -1) {
					if !helpFlag.MatchString(m[1]) {
						reply.StartAttempts++
					}
				}
			}
		case "result":
			resulted = true
			reply.Answer, reply.CostUSD, reply.Subagents = msg.Result, msg.Cost, msg.Subagents.Spawned
			for m, u := range msg.Usage {
				if reply.Model == "" || u.Out > msg.Usage[reply.Model].Out {
					reply.Model = m
				}
			}
			if msg.IsError {
				err = fmt.Errorf("claude: %s: %s", msg.Subtype, msg.Result)
			}
		}
	}
	return reply, resulted, err
}

// Result is one agent run of one scenario.
type Result struct {
	Scenario string        `json:"scenario"`
	Run      int           `json:"run"`
	Checks   []CheckResult `json:"checks"`
	Pass     bool          `json:"pass"`
	// Calls are the requests the agent made on the coordination socket.
	Calls []Call `json:"calls"`
	// Tools counts the agent's own tool calls by name.
	Tools     map[string]int `json:"tools,omitempty"`
	Subagents int            `json:"subagents"`
	Answer    string         `json:"answer"`
	Model     string         `json:"model,omitempty"`
	Latency   time.Duration  `json:"latency_ns"`
	CostUSD   *float64       `json:"cost_usd,omitempty"`
	Error     string         `json:"error,omitempty"`
	// Transcript is the harness's raw output, kept out of the JSON report.
	Transcript []byte `json:"-"`
}

// RunOnce runs scenario s once in a fresh staged session and scores it.
func RunOnce(ctx context.Context, agent Agent, helper string, s Scenario, n int) Result {
	res := Result{Scenario: s.ID, Run: n}
	l, err := Stage(ctx, helper, s)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer l.Close()
	start := time.Now()
	reply, err := agent(ctx, l)
	res.Latency, res.Answer, res.Model, res.CostUSD, res.Tools = time.Since(start), reply.Answer, reply.Model, reply.CostUSD, reply.Tools
	res.Subagents, res.Transcript = reply.Subagents, reply.Transcript
	if err != nil {
		res.Error = err.Error()
	}
	res.Calls = l.Coord.Calls()
	res.Checks = Score(s, res.Calls, reply.StartAttempts)
	res.Pass = err == nil
	for _, c := range res.Checks {
		res.Pass = res.Pass && c.Pass
	}
	return res
}
