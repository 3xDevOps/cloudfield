//go:build unix

package serversetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockConfig takes <resolved>.lock, created 0600 and, under root, given
// to the owner of the config file, or of its directory before the file
// exists, so the service that owns the config can take it too. Where it
// cannot be created, as when the directory is not writable, nothing is
// written.
func lockConfig(resolved string) (func(), error) {
	name := resolved + ".lock"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("serversetup: config lock: %w; nothing changed", err)
	}
	if os.Geteuid() == 0 {
		owner, serr := os.Stat(resolved)
		if errors.Is(serr, os.ErrNotExist) {
			owner, serr = os.Stat(filepath.Dir(resolved))
		}
		if serr == nil {
			uid, gid := fileOwner(owner)
			serr = f.Chown(uid, gid)
		}
		if serr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("serversetup: config lock %s: %w; nothing changed", name, serr)
		}
	}
	for deadline := time.Now().Add(ConfigLockWait); ; time.Sleep(50 * time.Millisecond) {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%s is still held after %s by another command writing the config; nothing changed", name, ConfigLockWait)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("serversetup: lock %s: %w", name, err)
	}
	return func() { _ = f.Close() }, nil
}
