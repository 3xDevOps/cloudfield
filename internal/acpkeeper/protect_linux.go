//go:build linux

package acpkeeper

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// agentOOMScoreAdj ranks the agent behind every process at the default
// score when the kernel picks an out-of-memory victim, unless the agent
// holds half the session's memory and swap more than that process.
const agentOOMScoreAdj = "-500"

// commandWrapper is the program an agent runs each of its commands through:
// it gives the command the default OOM score back, which the command would
// otherwise inherit from the agent, then runs it with bash as Claude Code
// documents for CLAUDE_CODE_SHELL_PREFIX.
const (
	commandWrapper       = "/run/aether-acp-command"
	commandWrapperScript = "#!/bin/sh\necho 0 > /proc/self/oom_score_adj\nexec bash -c \"$1\"\n"
)

// lowerOOMScore applies agentOOMScoreAdj to this process's parent, the
// managed exec the server tracks it by, and to this process, whose children
// inherit it. It needs CAP_SYS_RESOURCE, which root has in a session VM and
// not in a container.
func lowerOOMScore() error {
	for _, pid := range []string{strconv.Itoa(os.Getppid()), "self"} {
		if err := os.WriteFile("/proc/"+pid+"/oom_score_adj", []byte(agentOOMScoreAdj), 0); err != nil {
			return err
		}
	}
	return nil
}

// protectAgent lowers the OOM score the agent will inherit and returns the
// program the agent must run its commands through, so that they do not
// inherit it too.
func protectAgent() (string, error) {
	if _, err := exec.LookPath("bash"); err != nil {
		return "", fmt.Errorf("the command wrapper needs bash: %w", err)
	}
	// Claude Code may build its commands for zsh where there is one, and
	// the wrapper would run them with bash.
	if zsh, err := exec.LookPath("zsh"); err == nil {
		return "", fmt.Errorf("the command wrapper runs bash, and the agent may use %s", zsh)
	}
	tmp := commandWrapper + "." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(commandWrapperScript), 0o755); err != nil {
		return "", err
	}
	// WriteFile's mode is cut by the umask, and the agent's user runs it.
	err := os.Chmod(tmp, 0o755)
	if err == nil {
		err = os.Rename(tmp, commandWrapper)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := lowerOOMScore(); err != nil {
		return "", err
	}
	return commandWrapper, nil
}
