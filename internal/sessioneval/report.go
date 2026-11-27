package sessioneval

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Summary renders results as Markdown: a row per scenario with its pass rate
// and the subsessions asked for, the checks that failed, and every recorded
// call for a person to read.
func Summary(label string, scenarios []Scenario, results []Result) string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "# Session prompt eval: %s\n\n", label)
	fmt.Fprintln(w, "| scenario | pass | subsession starts | start attempts | wanted | harnesses | subagents | reports | by tool/command | nudges | socket calls | latency | cost |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	var pass, total int
	for _, s := range scenarios {
		var rs []Result
		for _, r := range results {
			if r.Scenario == s.ID {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}
		var p, calls, costed int
		var asked, attempts, harnesses, subagents, reported, via, nudges []string
		var lat time.Duration
		var cost float64
		for _, r := range rs {
			if r.Pass {
				p++
			}
			started := starts(r.Calls, true)
			asked = append(asked, fmt.Sprint(len(started)))
			attempts = append(attempts, fmt.Sprint(r.StartAttempts))
			for _, st := range started {
				if !slices.Contains(harnesses, st.Harness) {
					harnesses = append(harnesses, st.Harness)
				}
			}
			subagents = append(subagents, fmt.Sprint(r.Subagents))
			reported = append(reported, strings.Join(reports(r.Calls), "+"))
			via = append(via, fmt.Sprintf("%d/%d", r.ToolReports, r.CommandReports))
			nudges = append(nudges, fmt.Sprint(r.Nudges))
			calls += len(r.Calls)
			lat += r.Latency
			if r.CostUSD != nil {
				cost += *r.CostUSD
				costed++
			}
		}
		pass += p
		total += len(rs)
		n := len(rs)
		wanted := fmt.Sprintf("%d to %d", s.Check.MinStarts, s.Check.MaxStarts)
		if s.Check.MinStarts == s.Check.MaxStarts {
			wanted = fmt.Sprint(s.Check.MinStarts)
		}
		if s.Check.Harness != "" {
			wanted += " (" + s.Check.Harness + ")"
		}
		costCol := "not measured"
		if costed > 0 {
			costCol = fmt.Sprintf("$%.3f", cost/float64(costed))
		}
		fmt.Fprintf(w, "| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | %s | %d | %s | %s |\n", s.ID, p, n, strings.Join(asked, ", "), strings.Join(attempts, ", "), wanted,
			strings.Join(harnesses, ", "), strings.Join(subagents, ", "), strings.Join(reported, ", "), strings.Join(via, ", "), strings.Join(nudges, ", "), calls/n, (lat / time.Duration(n)).Round(time.Second), costCol)
	}
	fmt.Fprintf(w, "\n%d/%d runs passed. Subsession starts, start attempts, subagents, reports, reports by tool/command and nudges list one entry per run; socket calls, latency and cost are per-run means.\n", pass, total)

	fmt.Fprintln(w, "\n## Failed checks")
	for _, r := range results {
		var bad []string
		for _, c := range r.Checks {
			if !c.Pass {
				bad = append(bad, strings.TrimSpace(c.Name+" "+c.Detail))
			}
		}
		if r.Error != "" {
			bad = append(bad, "error: "+r.Error)
		}
		if len(bad) > 0 {
			fmt.Fprintf(w, "- %s #%d: %s\n", r.Scenario, r.Run, strings.Join(bad, "; "))
		}
	}

	fmt.Fprintln(w, "\n## Manual review")
	for _, s := range scenarios {
		if !slices.ContainsFunc(results, func(r Result) bool { return r.Scenario == s.ID }) {
			continue
		}
		fmt.Fprintf(w, "\n### %s\n\nPrompt: %q\n\n", s.ID, s.Prompt)
		for _, q := range s.Review {
			fmt.Fprintf(w, "- [ ] %s\n", q)
		}
		for _, r := range results {
			if r.Scenario != s.ID {
				continue
			}
			var tools []string
			for _, name := range slices.Sorted(maps.Keys(r.Tools)) {
				tools = append(tools, fmt.Sprintf("%s x%d", name, r.Tools[name]))
			}
			fmt.Fprintf(w, "\nRun %d, the agent's own tool calls: %s\n\n", r.Run, strings.Join(tools, ", "))
			for _, c := range r.Calls {
				line := c.Method
				if len(c.Params) > 0 {
					line += " " + string(c.Params)
				}
				switch {
				case c.Error != "":
					line += " -> refused: " + c.Error
				case c.Replay:
					line += " -> replayed receipt"
				}
				fmt.Fprintf(w, "- `%s`\n", strings.ReplaceAll(line, "`", "'"))
			}
			fmt.Fprintf(w, "- reply: %q\n", strings.Join(strings.Fields(r.Answer), " "))
		}
	}
	return w.String()
}
