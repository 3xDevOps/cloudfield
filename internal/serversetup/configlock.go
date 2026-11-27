package serversetup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ConfigLockWait is how long a config write waits for another holder of
// the config lock before it gives up.
var ConfigLockWait = 10 * time.Second

// UpdateConfig reads the config file at path and calls edit with its
// values and write, which writes them back, holding the config lock:
// <file>.lock beside the file path resolves to, so every name of one file
// takes the same lock. Every config write of this binary takes it.
func UpdateConfig(path string, edit func(values map[string]string, write func() error) error) error {
	return WithConfigLock(path, func(resolved string, write func(map[string]string) error) error {
		values, err := Load(resolved)
		if err != nil {
			return err
		}
		return edit(values, func() error { return write(values) })
	})
}

// WriteConfig replaces the config file at path with values, whatever it
// held, creating its directory if needed, holding the config lock.
func WriteConfig(path string, values map[string]string) error {
	return WithConfigLock(path, func(_ string, write func(map[string]string) error) error { return write(values) })
}

// WithConfigLock calls fn with the file path resolves to and write, which
// replaces that file with values, while holding the config lock. It reads
// nothing, so fn may face a file that does not parse.
func WithConfigLock(path string, fn func(resolved string, write func(values map[string]string) error) error) error {
	resolved, err := resolveConfig(path)
	if err != nil {
		return err
	}
	unlock, err := lockConfig(resolved)
	if err != nil {
		return err
	}
	defer unlock()
	return fn(resolved, func(values map[string]string) error { return writeConfig(resolved, values) })
}

// resolveConfig is the absolute path of the file path names, following
// symbolic links, or where it is created when it does not exist yet.
func resolveConfig(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("serversetup: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("serversetup: %w", err)
	}
	if _, lerr := os.Lstat(path); lerr == nil {
		return "", fmt.Errorf("serversetup: %s is a symbolic link to a missing file", path)
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("serversetup: create %s: %w", dir, err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return "", fmt.Errorf("serversetup: %w", err)
	}
	return filepath.Join(dir, filepath.Base(path)), nil
}
