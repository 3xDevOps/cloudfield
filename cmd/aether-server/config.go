package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/3xDevOps/cloudfield/internal/server"
	"github.com/3xDevOps/cloudfield/internal/serversetup"
)

func configCmd(args []string) error {
	// The subcommand comes off the front before flags are parsed: Go's
	// flag package stops at the first non-flag argument, so parsing first
	// would silently drop a --config that follows "show" or "set".
	if len(args) == 0 {
		return errors.New("usage: cloudfield-server config <show|path|set|edit> [--config <file>]")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	path := fs.String("config", serversetup.DefaultConfigPath, "options file to read or write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	// Only what follows the key and value is parsed as flags, so a value
	// such as -1 is never read as one.
	if sub == "set" && len(rest) > 2 {
		if err := fs.Parse(rest[2:]); err != nil {
			return err
		}
		rest = append(rest[:2:2], fs.Args()...)
	}
	switch sub {
	case "show":
		return configShow(os.Stdout, *path)
	case "path":
		fmt.Println(*path)
		return nil
	case "set":
		if len(rest) != 2 {
			return errors.New("usage: cloudfield-server config set <key> <value>")
		}
		return configSet(os.Stdout, *path, rest[0], rest[1])
	case "edit":
		return configEdit(os.Stdout, *path)
	default:
		return fmt.Errorf("unknown config command %q (want show, path, set, or edit)", sub)
	}
}

// configShow prints every server option with the value the next `serve`
// would use, marking which ones the config file is responsible for.
func configShow(w io.Writer, path string) error {
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	fs := serveFlagSet()
	retired, err := serversetup.Apply(fs, values)
	if err != nil {
		return err
	}
	for _, key := range retired {
		_, _ = fmt.Fprintf(w, "note: %q is no longer an option and is ignored; drop the line with `aether-server config edit`\n", key)
	}
	if len(values) == 0 {
		_, _ = fmt.Fprintf(w, "%s (no config file; every option is at its default)\n\n", path)
	} else {
		_, _ = fmt.Fprintf(w, "%s\n\n", path)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OPTION\tVALUE\tSOURCE")
	fs.VisitAll(func(f *flag.Flag) {
		source := "default"
		if _, ok := values[f.Name]; ok {
			source = "config"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", f.Name, f.Value.String(), source)
	})
	return tw.Flush()
}

// configSet rewrites one key, leaving every other key in the file as it is.
// The key and value are validated against the real `serve` options, so the
// file can never hold something the server would reject at startup.
func configSet(w io.Writer, path, key, value string) error {
	return configSetIf(w, path, key, value, nil)
}

// configSetIf is configSet with guard, when set, deciding on the values
// the file holds, under the config lock: it calls set to make the change,
// or returns why not.
func configSetIf(w io.Writer, path, key, value string, guard func(values map[string]string, set func() error) error) error {
	fs := serveFlagSet()
	if fs.Lookup(key) == nil {
		return fmt.Errorf("unknown option %q (known: %s)", key, joinOptions())
	}
	if err := fs.Set(key, value); err != nil {
		return fmt.Errorf("invalid value for %q: %w", key, err)
	}
	var was string
	err := updateConfig(path, func(values map[string]string, write func() error) error {
		set := func() error {
			v, ok := values[key]
			was = v
			if !ok {
				was = "unset"
				if def := fs.Lookup(key).DefValue; def != "" {
					was += ", meaning " + def
				}
			}
			values[key] = value
			return write()
		}
		if guard == nil {
			return set()
		}
		return guard(values, set)
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "%s: %s = %s (was %s)\n", path, key, value, was)
	apply := sudoPrefix() + serversetup.RestartCommand
	if key == "session-runtime" && value == server.SessionRuntimeVM {
		apply = sudoPrefix() + "aether-server vm install" + configFlag(path) + " && " + apply
	}
	_, _ = fmt.Fprintf(w, "apply it with:\n  %s\n", apply)
	return nil
}

// configLockWait is how long updateConfig waits for another holder of the
// config lock before it gives up.
var configLockWait = 10 * time.Second

// updateConfig reads the config file at path and calls edit with its values
// and write, which writes them back, holding <path>.lock: a file only
// whoever can write beside the config creates, mode 0600, which config set
// and edge move take. Where it cannot be created, as when the directory is
// not writable, nothing is written.
func updateConfig(path string, edit func(values map[string]string, write func() error) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	name := path + ".lock"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("config lock: %w; nothing changed", err)
	}
	defer f.Close() //nolint:errcheck // closing releases the lock
	for deadline := time.Now().Add(configLockWait); ; time.Sleep(50 * time.Millisecond) {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still held after %s by another config set or edge move; nothing changed", name, configLockWait)
		}
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", name, err)
	}
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	return edit(values, func() error { return serversetup.WriteConfig(path, values) })
}

// configEdit opens the config file in the operator's editor, then reparses
// it so a syntax error or a bad key surfaces now rather than at the next
// restart.
func configEdit(w io.Writer, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := serversetup.WriteConfig(path, map[string]string{}); err != nil {
			return err
		}
	}
	// EDITOR commonly carries arguments ("code --wait", "emacs -nw"), so
	// it is split the way cmd/aether/agent.go splits its own command
	// strings. exec.Command never invokes a shell, so a value with shell
	// metacharacters is looked up as a literal name and fails safely.
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		fallback := "/usr/bin/editor"
		if _, err := os.Stat(fallback); err != nil {
			fallback = "vi"
		}
		editor = []string{fallback}
	}
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", strings.Join(editor, " "), path, err)
	}
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	retired, err := serversetup.Apply(serveFlagSet(), values)
	if err != nil {
		return err
	}
	for _, key := range retired {
		_, _ = fmt.Fprintf(w, "note: %q is no longer an option and is ignored\n", key)
	}
	_, _ = fmt.Fprintf(w, "apply it with:\n  %s\n", serversetup.RestartCommand)
	return nil
}

// serveFlagSet builds the `serve` options for validation only, with output
// discarded so a bad value returns an error instead of printing usage.
func serveFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serveFlags(fs)
	return fs
}

// joinOptions lists every known option name, for the error a typo produces
// and for the usage text.
func joinOptions() string {
	var names []string
	serveFlagSet().VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	slices.Sort(names)
	return strings.Join(names, ", ")
}
