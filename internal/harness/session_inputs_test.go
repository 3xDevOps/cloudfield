package harness

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeSessionInputsFollowWhatASessionReads(t *testing.T) {
	home := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest := func() string {
		t.Helper()
		d, err := claudeSessionInputs(home, "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	write(".claude/settings.json", `{"model":"opus"}`)
	write(".claude/CLAUDE.md", "rules\n@~/notes/style.md\n")
	write("notes/style.md", "plain dashes")
	write(".claude/skills/review/SKILL.md", "review")
	write(".claude.json", `{"numStartups":1,"pluginUsage":{"a":1},"mcpServers":{},"projects":{"/workspace":{"allowedTools":[],"lastCost":1},"/other":{"allowedTools":["x"]}}}`)
	before := digest()

	// What a running session rewrites: the same bytes in a settings file,
	// its own counters, and other projects' entries.
	write(".claude/settings.json", `{"model":"opus"}`)
	write(".claude.json", `{"numStartups":2,"pluginUsage":{"a":2},"mcpServers":{},"projects":{"/workspace":{"allowedTools":[],"lastCost":9},"/other":{"allowedTools":["y"]}}}`)
	write(".claude/plugins/cache/m/p/1/.in_use/42", "")
	if got := digest(); got != before {
		t.Fatal("the digest changed with files a session start rewrites")
	}

	changes := []struct {
		name   string
		change func()
	}{
		{"settings", func() { write(".claude/settings.json", `{"model":"sonnet"}`) }},
		{"instructions", func() { write(".claude/CLAUDE.md", "other rules\n@~/notes/style.md\n") }},
		{"imported file", func() { write("notes/style.md", "em dashes") }},
		{"plugins", func() { write(".claude/plugins/installed_plugins.json", `{"plugins":{}}`) }},
		{"new skill", func() { write(".claude/skills/audit/SKILL.md", "audit") }},
		{"edited skill", func() {
			if err := os.Chtimes(filepath.Join(home, ".claude/skills/review/SKILL.md"), time.Now(), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}},
		{"MCP server", func() {
			write(".claude.json", `{"mcpServers":{"db":{"command":"db"}},"projects":{"/workspace":{"allowedTools":[]}}}`)
		}},
		{"project's tool", func() {
			write(".claude.json", `{"mcpServers":{"db":{"command":"db"}},"projects":{"/workspace":{"allowedTools":["Bash"]}}}`)
		}},
	}
	for _, c := range changes {
		was := digest()
		c.change()
		if digest() == was {
			t.Errorf("the digest did not change with: %s", c.name)
		}
	}
}
