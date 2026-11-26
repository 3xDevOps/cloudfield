package sessioneval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func call(method string, params any, code int) Call {
	raw, _ := json.Marshal(params)
	return Call{Method: method, Params: raw, Code: code}
}

func start(harness string, code int) Call {
	return call(protocol.MethodChildStart, protocol.ChildStartParams{Harness: harness}, code)
}

func report(outcome string, code int) Call {
	return call(protocol.MethodCoordReport, protocol.CoordReportParams{Outcome: outcome}, code)
}

func TestScore(t *testing.T) {
	status := Call{Method: protocol.MethodCoordStatus}
	replayed := start("claude", 0)
	replayed.Replay = true
	for _, tc := range []struct {
		name     string
		checks   Checks
		parent   string
		calls    []Call
		attempts int
		failed   string
	}{
		{name: "no subsession and one report", calls: []Call{status, report("success", 0)}},
		{name: "a subsession nobody asked for", calls: []Call{start("claude", 0), report("waiting", 0)}, failed: "subsession starts"},
		{name: "a refused start still counts", parent: "run-parent", calls: []Call{start("claude", protocol.CodeDenied), report("success", 0)}, failed: "subsession starts"},
		{name: "a retry and a malformed start do not count", checks: Checks{MinStarts: 1, MaxStarts: 1},
			calls: []Call{start("", protocol.CodeInvalidParams), start("claude", 0), replayed, report("waiting", 0)}},
		{name: "a start the CLI refused before the socket", calls: []Call{report("success", 0)}, attempts: 1, failed: "subsession starts"},
		{name: "exactly two", checks: Checks{MinStarts: 2, MaxStarts: 2}, calls: []Call{start("claude", 0), start("codex", 0), report("waiting", 0)}},
		{name: "one where two were asked", checks: Checks{MinStarts: 2, MaxStarts: 2}, calls: []Call{start("claude", 0), report("waiting", 0)}, failed: "subsession starts"},
		{name: "the wrong agent", checks: Checks{MinStarts: 1, MaxStarts: 1, Harness: "codex"}, calls: []Call{start("claude", 0), report("waiting", 0)}, failed: "subsession harness"},
		{name: "no report", calls: []Call{status}, failed: "one report a turn,report outcome,report is the last call"},
		{name: "two reports", calls: []Call{report("answered", 0), report("success", 0)}, failed: "one report a turn"},
		{name: "waiting, then a later turn's report", calls: []Call{report("waiting", 0), report("success", 0)}},
		{name: "a refused report is not a report", calls: []Call{report("success", protocol.CodeInvalidState), report("waiting", 0)}},
		{name: "an outcome the card does not list", calls: []Call{report("done", 0)}, failed: "report outcome"},
		{name: "work after the report", calls: []Call{report("success", 0), status}, failed: "report is the last call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failed []string
			for _, c := range Score(Scenario{Check: tc.checks, Parent: tc.parent}, tc.calls, tc.attempts) {
				if !c.Pass {
					failed = append(failed, c.Name)
				}
			}
			if got := strings.Join(failed, ","); got != tc.failed {
				t.Fatalf("failed checks = %q, want %q", got, tc.failed)
			}
		})
	}
}

func TestScenarios(t *testing.T) {
	scenarios, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, s := range scenarios {
		if s.ID == "" || seen[s.ID] || s.Prompt == "" || s.Check.MinStarts > s.Check.MaxStarts {
			t.Errorf("scenario %+v needs a unique id, a prompt and min_starts <= max_starts", s)
		}
		seen[s.ID] = true
	}
}

func TestParseStream(t *testing.T) {
	out := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"on it"},{"type":"tool_use","name":"Bash","input":{}}]}}
{"type":"user","message":{"content":"plain text"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Edit"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"aether-internal subsession start --help; cloudfield-internal subsession start --harness codex -h"}},{"type":"tool_use","name":"Bash","input":{"command":"aether-internal skill subsessions | grep 'subsession start'"}},{"type":"tool_use","name":"Bash","input":{"command":"aether-internal subsession start --harness codex"}},{"type":"tool_use","name":"mcp__aether__aether_subsession_start"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"fixed","total_cost_usd":0.25,"subagent_stats":{"spawned":2},"modelUsage":{"small":{"outputTokens":3},"large":{"outputTokens":90}}}
`
	reply, resulted, err := parseStream([]byte(out))
	if err != nil || !resulted {
		t.Fatalf("parseStream: resulted %v, err %v", resulted, err)
	}
	if reply.Answer != "fixed" || reply.Model != "large" || reply.CostUSD == nil || *reply.CostUSD != 0.25 || reply.Tools["Bash"] != 5 || reply.Tools["Edit"] != 1 || reply.Subagents != 2 || reply.StartAttempts != 2 {
		t.Fatalf("reply = %+v", reply)
	}
	if _, resulted, err := parseStream([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`)); !resulted || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("an error result: resulted %v, err %v", resulted, err)
	}
	if _, resulted, _ := parseStream([]byte("not json\n")); resulted {
		t.Fatal("output without a result line counted as a result")
	}
}
