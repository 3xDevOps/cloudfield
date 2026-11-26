package sessioneval

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/cloudfield/internal/coordcli"
	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/mcpbridge"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func stage(t *testing.T, s Scenario) *Launch {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	l, err := Stage(t.Context(), helper, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

// cli runs the staged in-container command the way the agent's shell does.
func cli(t *testing.T, l *Launch, args ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", append([]string{"-c", coordhooks.OldCLI + ` "$@"`, "sh"}, args...)...)
	// A binary built with -race otherwise sleeps a second each time it exits.
	cmd.Dir, cmd.Env = l.Repo, append(l.Env, "GORACE=atexit_sleep_ms=0")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("%s %v: %v", coordhooks.OldCLI, args, err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

func methods(calls []Call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Method
	}
	return out
}

func TestStagedSessionThroughTheRealCommand(t *testing.T) {
	l := stage(t, Scenario{Prompt: "fix the discount"})

	if out, err := exec.CommandContext(t.Context(), "git", "-C", l.Repo, "status", "--porcelain").CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("the fixture is not a clean committed repository: %v: %s", err, out)
	}
	if !strings.Contains(l.Instructions, "skill subsessions` says how") {
		t.Fatalf("the instructions are not the role card of a session that may start subsessions:\n%s", l.Instructions)
	}

	skill, code := cli(t, l, "skill")
	for _, want := range []string{"Run: " + runID, "Task summary (bounded): fix the discount", "When to start one, the steps, the task format and the account rules", "- waiting: ", "Inbox hooks (claude): loaded by this session's launch"} {
		if code != coordcli.ExitOK || !strings.Contains(skill, want) {
			t.Fatalf("skill (exit %d) lacks %q:\n%s", code, want, skill)
		}
	}

	if out, code := cli(t, l, "subsession", "start", "--harness", "codex", "--purpose", "review the fix", "--task", "review HEAD", "--idempotency-key", "k1"); code != coordcli.ExitOK {
		t.Fatalf("subsession start: exit %d: %s", code, out)
	}
	if out, code := cli(t, l, "subsession", "start", "--harness", "codex", "--purpose", "review the fix", "--task", "review HEAD", "--idempotency-key", "k1"); code != coordcli.ExitOK {
		t.Fatalf("subsession start retry: exit %d: %s", code, out)
	}
	if out, code := cli(t, l, "subsession", "start", "--harness", "claude", "--purpose", "review the fix", "--task", "review HEAD", "--idempotency-key", "k1"); code != coordcli.ExitDenied || !strings.Contains(out, "use a new key") {
		t.Fatalf("a reused key with another agent: exit %d: %s", code, out)
	}
	if out, code := cli(t, l, "report", "success", "--summary", "done"); code != coordcli.ExitDenied || !strings.Contains(out, "1 subsessions are unfinished") {
		t.Fatalf("success with a running subsession: exit %d: %s", code, out)
	}
	if out, code := cli(t, l, "report", "waiting", "--for", "agents", "--summary", "the codex review"); code != coordcli.ExitOK {
		t.Fatalf("report waiting: exit %d: %s", code, out)
	}

	calls := l.Coord.Calls()
	want := []string{protocol.MethodCoordStatus, protocol.MethodChildStart, protocol.MethodChildStart, protocol.MethodChildStart, protocol.MethodCoordReport, protocol.MethodCoordReport}
	if got := methods(calls); !slices.Equal(got, want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	if !calls[2].Replay || calls[3].Code != protocol.CodeConflict || calls[4].Code != protocol.CodeInvalidState {
		t.Fatalf("the retry, the reused key and the refused report are not marked: %+v", calls[2:5])
	}
	for _, c := range Score(Scenario{Check: Checks{MinStarts: 1, MaxStarts: 1, Harness: "codex"}}, calls, 0) {
		if !c.Pass {
			t.Errorf("check %q failed: %s", c.Name, c.Detail)
		}
	}

	for _, args := range [][]string{{"subsession", "start", "--help"}, {"subsession", "start", "--harness", "codex"}, {"skill", "subsessions"}} {
		cli(t, l, args...)
	}
	if n, err := l.CommandStarts(); err != nil || n != 4 {
		t.Fatalf("CommandStarts = %d, %v, want the three sent, the one the command refused and no help request", n, err)
	}
}

func TestSubsessionCannotStartSubsessions(t *testing.T) {
	l := stage(t, Scenario{Prompt: "fix the discount", Parent: "run-parent"})
	if !strings.Contains(l.Instructions, "Session run-parent started you as its subsession") || strings.Contains(l.Instructions, "skill subsessions` says how") {
		t.Fatalf("the instructions are not a subsession's role card:\n%s", l.Instructions)
	}

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
	server := config.Servers[mcpbridge.ServerName]
	bridge := exec.CommandContext(t.Context(), server.Command)
	bridge.Env = l.Env
	for name, value := range server.Env {
		bridge.Env = append(bridge.Env, name+"="+value)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "sessioneval-test"}, nil).Connect(t.Context(), &mcp.CommandTransport{Command: bridge}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close() //nolint:errcheck // the test is over

	if got, want := cs.InitializeResult().Instructions, coordhooks.Brief(Scenario{Parent: "run-parent"}.Guide()); got != want {
		t.Fatalf("bridge instructions = %q, want the brief %q", got, want)
	}
	var status protocol.CoordStatusResult
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_status"})
	if err != nil || res.IsError {
		t.Fatalf("aether_status: %v, %+v", err, res)
	}
	structured, _ := json.Marshal(res.StructuredContent)
	if err = json.Unmarshal(structured, &status); err != nil {
		t.Fatal(err)
	}
	if status.ParentRunID != "run-parent" || slices.Contains(status.Capabilities, protocol.MethodChildStart) || !slices.Contains(status.Capabilities, protocol.MethodChildOptions) {
		t.Fatalf("status = %+v", status)
	}
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_subsession_start", Arguments: protocol.ChildStartParams{
		Harness: "claude", Purpose: "review the fix", Task: "review HEAD", IdempotencyKey: "k1"}})
	if err != nil || !res.IsError {
		t.Fatalf("a subsession's aether_subsession_start: err %v, result %+v", err, res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "subsessions go one level deep") {
		t.Fatalf("the refusal = %q", text)
	}

	calls := l.Coord.Calls()
	if last := calls[len(calls)-1]; last.Method != protocol.MethodChildStart || last.Code != protocol.CodeDenied {
		t.Fatalf("the refused start is not recorded: %+v", last)
	}
	if got := Score(Scenario{Parent: "run-parent"}, calls, 0)[0]; got.Pass {
		t.Fatalf("a subsession that asked for a subsession passed: %+v", got)
	}
}
