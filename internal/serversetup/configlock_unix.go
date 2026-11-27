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

// lockConfig takes <resolved>.lock. It creates the file 0600 and, under
// root, gives only a file it created to the owner of the config file, or
// of its directory before the file exists, so the service that owns the
// config can take it too. An existing lock file must be a regular file
// with one link, owned by root or by that owner: whoever owns the
// directory could plant the name as a link to another file. Where it
// cannot be created, as when the directory is not writable, nothing is
// written.
func lockConfig(resolved string) (func(), error) {
	name := resolved + ".lock"
	uid, gid, err := configOwner(resolved)
	if err != nil {
		return nil, fmt.Errorf("serversetup: config lock %s: %w; nothing changed", name, err)
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	switch {
	case err == nil && os.Geteuid() == 0:
		err = f.Chown(uid, gid)
	case errors.Is(err, os.ErrExist):
		if f, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0); err == nil {
			err = checkLockFile(f, uid)
		}
	}
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		return nil, fmt.Errorf("serversetup: config lock: %w; nothing changed", err)
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

// configOwner is the owner of the config file, or of its directory before
// the file exists.
func configOwner(resolved string) (uid, gid int, err error) {
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		info, err = os.Stat(filepath.Dir(resolved))
	}
	if err != nil {
		return -1, -1, err
	}
	uid, gid = fileOwner(info)
	return uid, gid, nil
}

// checkLockFile refuses an existing lock file that is not a regular file
// with one link owned by root or owner.
func checkLockFile(f *os.File, owner int) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || st.Nlink != 1 || st.Uid != 0 && int(st.Uid) != owner {
		return fmt.Errorf("%s is not a regular file with one link owned by root or the config's owner; remove it and run this again", f.Name())
	}
	return nil
}
