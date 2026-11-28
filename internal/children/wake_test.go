package children

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

const antigravityRefusal = "antigravity in Standard mode has no idle wake, and it has no Enhanced mode; as a subsession it runs only in Background, and a session that waits for mail needs another agent"

func TestParentsMustWakeOnMail(t *testing.T) {
	h := newHarness(t)
	standard := func(agent string) domain.RunID {
		parent := h.run(h.ada.ID, domain.LaunchTUI, "standard "+agent)
		parent.Harness = agent
		if err := h.db.UpdateRun(h.ctx, parent); err != nil {
			t.Fatal(err)
		}
		return parent.ID
	}
	_, err := h.start(standard("antigravity"), protocol.ChildStartParams{Harness: "codex"})
	if code(err) != protocol.CodeDenied || !strings.HasSuffix(err.Error(), antigravityRefusal) {
		t.Fatalf("start from a Standard antigravity session = %v, want it refused with %q", err, antigravityRefusal)
	}
	h.mustStart(standard("gemini"), protocol.ChildStartParams{Harness: "codex"})
}

func TestSubsessionsMustWakeOnMail(t *testing.T) {
	h := newHarness(t)
	h.agents.more = []protocol.AgentInfo{{Name: "gemini", Installed: true, EnhancedInstalled: true}, {Name: "antigravity", Installed: true}}
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")

	_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "antigravity", Mode: "standard"})
	if code(err) != protocol.CodeDenied || !strings.HasSuffix(err.Error(), antigravityRefusal) {
		t.Fatalf("a Standard antigravity subsession = %v, want it refused with %q", err, antigravityRefusal)
	}
	if got := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "gemini", Mode: "standard"}); got.Mode != "standard" {
		t.Fatalf("a Standard gemini subsession runs %s, want standard", got.Mode)
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
	for agent, modes := range map[string][]string{"claude": {"enhanced", "standard", "background"}, "gemini": {"enhanced", "standard", "background"}, "antigravity": {"background"}} {
		if c := offered[agent]; !slices.Equal(c.Modes, modes) || c.DefaultMode != modes[0] {
			t.Fatalf("%s offers %v, default %q; want %v, default %q", agent, c.Modes, c.DefaultMode, modes, modes[0])
		}
	}
}
