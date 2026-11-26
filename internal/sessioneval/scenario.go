package sessioneval

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

//go:embed testdata/scenarios.json
var scenariosJSON []byte

const runID = "run-eval"

// Scenario is one task given to a session and how many subsessions a
// session that follows its role card starts for it.
type Scenario struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// Parent is the run that started this session as its subsession; empty
	// for a session a person started.
	Parent string `json:"parent,omitempty"`
	Check  Checks `json:"checks"`
	// Review lists what a person should read in the output; the checks
	// cannot judge it.
	Review []string `json:"review,omitempty"`
}

// Checks are a scenario's deterministic expectations.
type Checks struct {
	MinStarts int `json:"min_starts"`
	MaxStarts int `json:"max_starts"`
	// Harness is the agent at least one started subsession must run.
	Harness string `json:"harness,omitempty"`
}

// Load returns the embedded scenarios.
func Load() ([]Scenario, error) {
	var f struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(scenariosJSON, &f); err != nil {
		return nil, fmt.Errorf("sessioneval: scenarios: %w", err)
	}
	return f.Scenarios, nil
}

// Guide is the guide of the scenario's session: a Standard Claude Code
// session whose launch loaded the mail integration.
func (s Scenario) Guide() coordhooks.Guide {
	return coordhooks.Guide{RunID: runID, Role: coordhooks.RoleInteractive, Wake: true, Parent: s.Parent, CLI: coordhooks.LocalCLI(coordtransport.CloudfieldCLIPath)}
}

// CheckResult is one check's verdict.
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// starts are the subsessions the agent asked for: the accepted child.start
// requests and, when denied is true, the ones policy refused. A retry of an
// idempotency key and a request with invalid parameters are not counted.
func starts(calls []Call, denied bool) []protocol.ChildStartParams {
	var out []protocol.ChildStartParams
	for _, c := range calls {
		if c.Method != protocol.MethodChildStart || c.Replay || c.Code != 0 && (c.Code != protocol.CodeDenied || !denied) {
			continue
		}
		var p protocol.ChildStartParams
		_ = json.Unmarshal(c.Params, &p)
		out = append(out, p)
	}
	return out
}

// reports are the outcomes of the accepted coord.report requests.
func reports(calls []Call) []string {
	var out []string
	for _, c := range calls {
		if c.Method != protocol.MethodCoordReport || c.Replay || c.Code != 0 {
			continue
		}
		var p protocol.CoordReportParams
		_ = json.Unmarshal(c.Params, &p)
		out = append(out, p.Outcome)
	}
	return out
}

// Score checks the recorded calls of one run against the scenario. attempts
// is how many times the agent tried to start a subsession; where none is
// wanted, each counts, whatever became of it.
func Score(s Scenario, calls []Call, attempts int) []CheckResult {
	var res []CheckResult
	add := func(name string, pass bool, detail string) {
		res = append(res, CheckResult{Name: name, Pass: pass, Detail: detail})
	}
	asked := len(starts(calls, true))
	if s.Check.MaxStarts == 0 {
		asked = max(asked, attempts)
	}
	want := fmt.Sprintf("%d to %d", s.Check.MinStarts, s.Check.MaxStarts)
	if s.Check.MinStarts == s.Check.MaxStarts {
		want = fmt.Sprint(s.Check.MinStarts)
	}
	add("subsession starts", asked >= s.Check.MinStarts && asked <= s.Check.MaxStarts, fmt.Sprintf("want %s, got %d", want, asked))
	if s.Check.Harness != "" {
		var got []string
		for _, p := range starts(calls, false) {
			got = append(got, p.Harness)
		}
		add("subsession harness", slices.Contains(got, s.Check.Harness), fmt.Sprintf("want %s, got %q", s.Check.Harness, got))
	}

	var allowed []string
	for _, o := range coordhooks.Outcomes(s.Guide()) {
		allowed = append(allowed, strings.Split(o.Name, " | ")...)
	}
	reported := reports(calls)
	earlier := reported[:max(len(reported)-1, 0)]
	add("one report a turn", len(reported) > 0 && !slices.ContainsFunc(earlier, func(o string) bool { return o != "waiting" }),
		fmt.Sprintf("want only waiting before the last, got %q", reported))
	ok := len(reported) > 0
	for _, outcome := range reported {
		ok = ok && slices.Contains(allowed, outcome)
	}
	add("report outcome", ok, fmt.Sprintf("want one of %s", strings.Join(allowed, ", ")))
	last := len(calls) > 0 && calls[len(calls)-1].Method == protocol.MethodCoordReport && calls[len(calls)-1].Code == 0
	add("report is the last call", last, "")
	return res
}
