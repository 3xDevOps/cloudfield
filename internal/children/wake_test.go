package children

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

var wakeRefusals = map[string]string{
	"gemini":      "gemini in Standard mode has no idle wake; start it in Enhanced",
	"antigravity": "antigravity in Standard mode has no idle wake, and it has no Enhanced mode; as a subsession it runs only in Background, and a session that waits for mail needs another agent",
}

func TestParentsMustWakeOnMail(t *testing.T) {
	h := newHarness(t)
	for agent, want := range wakeRefusals {
		parent := h.run(h.ada.ID, domain.LaunchTUI, "standard "+agent)
		parent.Harness = agent
		if err := h.db.UpdateRun(h.ctx, parent); err != nil {
			t.Fatal(err)
		}
		_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"})
		if code(err) != protocol.CodeDenied || !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("start from a Standard %s session = %v, want it refused with %q", agent, err, want)
		}
	}
}

func TestSubsessionsMustWakeOnMail(t *testing.T) {
	h := newHarness(t)
	h.agents.more = []protocol.AgentInfo{{Name: "gemini", Installed: true, EnhancedInstalled: true}, {Name: "antigravity", Installed: true}}
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")

	for agent, want := range wakeRefusals {
		_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: agent, Mode: "standard"})
		if code(err) != protocol.CodeDenied || !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("a Standard %s subsession = %v, want it refused with %q", agent, err, want)
		}
	}
	for _, mode := range []string{"", "background"} {
		if got := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "antigravity", Mode: mode}); got.Mode != "background" {
			t.Fatalf("an antigravity subsession with mode %q runs %s, want background", mode, got.Mode)
		}
	}

	raw, _ := json.Marshal(protocol.ChildOptionsParams{})
	out, err := h.svc.HandleAgent(h.ctx, parent.ID, protocol.MethodChildOptions, raw)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]protocol.ChildChoice{}
	for _, c := range out.(protocol.ChildOptionsResult).Choices {
		offered[c.Harness] = c
	}
	for agent, modes := range map[string][]string{"claude": {"enhanced", "standard", "background"}, "gemini": {"enhanced", "background"}, "antigravity": {"background"}} {
		if c := offered[agent]; !slices.Equal(c.Modes, modes) || c.DefaultMode != modes[0] {
			t.Fatalf("%s offers %v, default %q; want %v, default %q", agent, c.Modes, c.DefaultMode, modes, modes[0])
		}
	}
}
