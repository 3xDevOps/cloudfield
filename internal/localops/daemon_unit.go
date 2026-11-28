package localops

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// InstalledDaemonArgs are the `daemon run` flags of the installed sync
// daemon unit, as InstallDaemonUnit wrote them, nil when none is
// installed.
func InstalledDaemonArgs() ([]string, error) {
	installed, path, err := DaemonStatus()
	if err != nil || !installed {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	args, err := unitArgs(runtime.GOOS, string(body))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return args, nil
}

// unitArgs reverses syncd.ServiceUnit for goos: the argv after the binary
// and its "daemon run".
func unitArgs(goos, content string) ([]string, error) {
	var (
		argv []string
		err  error
	)
	switch goos {
	case "linux":
		argv, err = systemdArgs(content)
	case "darwin":
		argv, err = plistArgs(content)
	case "windows":
		argv, err = taskArgs(content)
	default:
		return nil, fmt.Errorf("no service template for OS %q", goos)
	}
	if err != nil {
		return nil, err
	}
	if len(argv) < 3 || argv[1] != "daemon" || argv[2] != "run" {
		return nil, errors.New("the unit does not run the sync daemon")
	}
	return argv[3:], nil
}

func systemdArgs(content string) ([]string, error) {
	for line := range strings.SplitSeq(content, "\n") {
		if exec, ok := strings.CutPrefix(line, "ExecStart="); ok {
			return systemdWords(exec)
		}
	}
	return nil, errors.New("the unit has no ExecStart line")
}

// systemdWords splits an ExecStart line quoted as syncd's systemdQuote
// quotes it.
func systemdWords(line string) ([]string, error) {
	var words []string
	for line = strings.TrimLeft(line, " "); line != ""; line = strings.TrimLeft(line, " ") {
		if line[0] != '"' {
			word, rest, _ := strings.Cut(line, " ")
			words, line = append(words, word), rest
			continue
		}
		var b strings.Builder
		i := 1
		for ; i < len(line) && line[i] != '"'; i++ {
			switch {
			case line[i] == '\\' && i+1 < len(line):
				i++
			case (line[i] == '$' || line[i] == '%') && i+1 < len(line) && line[i+1] == line[i]:
				i++
			}
			b.WriteByte(line[i])
		}
		if i == len(line) {
			return nil, errors.New("the ExecStart line has an unterminated quote")
		}
		words, line = append(words, b.String()), line[i+1:]
	}
	return words, nil
}

func plistArgs(content string) ([]string, error) {
	var plist struct {
		Strings []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal([]byte(content), &plist); err != nil {
		return nil, fmt.Errorf("parse the plist: %w", err)
	}
	return plist.Strings, nil
}

func taskArgs(content string) ([]string, error) {
	var task struct {
		Command   string `xml:"Actions>Exec>Command"`
		Arguments string `xml:"Actions>Exec>Arguments"`
	}
	if err := xml.Unmarshal([]byte(content), &task); err != nil {
		return nil, fmt.Errorf("parse the task: %w", err)
	}
	words, err := windowsWords(task.Arguments)
	if err != nil {
		return nil, err
	}
	return append([]string{task.Command}, words...), nil
}

// windowsWords splits Arguments quoted as syncd's windowsArgs quotes
// them: a word with a space or quote is double-quoted, an embedded quote
// doubled.
func windowsWords(line string) ([]string, error) {
	var words []string
	for line = strings.TrimLeft(line, " "); line != ""; line = strings.TrimLeft(line, " ") {
		if line[0] != '"' {
			word, rest, _ := strings.Cut(line, " ")
			words, line = append(words, word), rest
			continue
		}
		var b strings.Builder
		i := 1
		for ; i < len(line); i++ {
			if line[i] == '"' {
				if i+1 < len(line) && line[i+1] == '"' {
					i++
				} else {
					break
				}
			}
			b.WriteByte(line[i])
		}
		if i == len(line) {
			return nil, errors.New("the task arguments have an unterminated quote")
		}
		words, line = append(words, b.String()), line[i+1:]
	}
	return words, nil
}
