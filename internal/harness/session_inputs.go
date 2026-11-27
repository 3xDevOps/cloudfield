package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// claudeInputFiles and claudeInputDirs are what Claude Code 2.1.332 read in
// its home while it made a session, traced with file access times. Files are
// hashed by content, because Claude Code rewrites some with the same bytes
// at every start; directories by each entry's path, size and modification
// time. Left out on purpose: ~/.claude.json as a whole, which every running
// session rewrites, the plugin cache, pinned by installed_plugins.json, and
// the login file, which is part of the VM's mounts.
var (
	claudeInputFiles = []string{
		".claude/settings.json",
		".claude/CLAUDE.md",
		".claude/plugins/installed_plugins.json",
		".claude/plugins/known_marketplaces.json",
		".claude/state/mcp-discover-verdicts.json",
	}
	claudeInputDirs = []string{
		".claude/rules", ".claude/agents", ".claude/commands", ".claude/skills", ".claude/output-styles",
	}
	// claudeProjectKeys are the keys of a project's entry in ~/.claude.json
	// a new session reads its tools and MCP servers from.
	claudeProjectKeys = []string{"mcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "allowedTools", "hasTrustDialogAccepted"}
	claudeImport      = regexp.MustCompile(`(?m)(?:^|\s)@((?:~/|/)[^\s]+)`)
)

// claudeImportDepth is how many files deep Claude Code follows an import.
const claudeImportDepth = 5

// claudeSessionInputs digests what a Claude Code session started in project
// reads from home, so a session made ahead is known to match one made now.
func claudeSessionInputs(home, project string) (string, error) {
	h := sha256.New()
	for _, rel := range claudeInputFiles {
		if err := hashFile(h, home, filepath.Join(home, rel)); err != nil {
			return "", err
		}
	}
	if err := hashImports(h, home, filepath.Join(home, ".claude/CLAUDE.md"), claudeImportDepth); err != nil {
		return "", err
	}
	for _, rel := range claudeInputDirs {
		if err := hashTree(h, filepath.Join(home, rel)); err != nil {
			return "", err
		}
	}
	if err := hashClaudeConfig(h, filepath.Join(home, ".claude.json"), project); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(h io.Writer, home, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = nil
	} else if err != nil {
		return fmt.Errorf("harness: read session input: %w", err)
	}
	rel, _ := filepath.Rel(home, path)
	fmt.Fprintf(h, "file %q %d %t\n", rel, len(data), err == nil)
	_, _ = h.Write(data)
	return nil
}

// hashImports adds the files under home, or at an absolute path, that the
// instructions file at path imports with @path, and theirs.
func hashImports(h io.Writer, home, path string, depth int) error {
	data, err := os.ReadFile(path)
	if depth == 0 || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("harness: read session input: %w", err)
	}
	for _, m := range claudeImport.FindAllSubmatch(data, -1) {
		target := string(m[1])
		if rest, ok := strings.CutPrefix(target, "~/"); ok {
			target = filepath.Join(home, rest)
		}
		if err := hashFile(h, home, target); err != nil {
			return err
		}
		if err := hashImports(h, home, target, depth-1); err != nil {
			return err
		}
	}
	return nil
}

func hashTree(h io.Writer, root string) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "entry %q %d %d %s\n", path, info.Size(), info.ModTime().UnixNano(), info.Mode())
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("harness: list session input: %w", err)
	}
	return nil
}

func hashClaudeConfig(h io.Writer, path, project string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("harness: read session input: %w", err)
	}
	var config struct {
		MCPServers json.RawMessage                       `json:"mcpServers"`
		Projects   map[string]map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("harness: decode %s: %w", filepath.Base(path), err)
	}
	fmt.Fprintf(h, "mcpServers %s\n", config.MCPServers)
	for _, key := range claudeProjectKeys {
		fmt.Fprintf(h, "project %s %s\n", key, config.Projects[project][key])
	}
	return nil
}
