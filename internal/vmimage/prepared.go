package vmimage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// NewCheckoutDisk creates a qcow2 disk at path over the prepared disk
// cached under key, building that on first use: an ext4 filesystem of size
// bytes holding what fill puts in an empty directory. Concurrent first uses
// share one build, which a cancelled caller leaves running for the others:
// fill gets the build's context. A prepared disk is swept once no disk
// backs onto it and KeepCheckoutDisk does not keep it.
func (s *Store) NewCheckoutDisk(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error, path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("vmimage: checkout disk: %w", err)
	}
	base, err := s.pinPrepared(ctx, key, size, fill, path+"."+dirName(preparedPrefix+key)+".base")
	if err != nil {
		return err
	}
	_, err = s.NewRootDisk(ctx, base, path)
	return err
}

// KeepCheckoutDisk builds the prepared disk under key as NewCheckoutDisk
// does and keeps it from the sweep, in place of the disk kept before under
// the same group and slot.
func (s *Store) KeepCheckoutDisk(ctx context.Context, group, slot, key string, size int64, fill func(ctx context.Context, dir string) error) error {
	if !validPreparedKey(group) || !validPreparedKey(slot) {
		return fmt.Errorf("vmimage: kept disk %q of %q must be letters, digits, - or _", slot, group)
	}
	_, err := s.pinPrepared(ctx, key, size, fill, filepath.Join(s.keptDir(), group, slot))
	return err
}

// KeptCheckoutDisks lists the slots of group that keep a prepared disk.
func (s *Store) KeptCheckoutDisks(group string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.keptDir(), group))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("vmimage: kept prepared disks of %s: %w", group, err)
	}
	slots := make([]string, 0, len(entries))
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".tmp" {
			slots = append(slots, e.Name())
		}
	}
	return slots, nil
}

// ForgetCheckoutDisks stops keeping the prepared disks of group, which the
// next sweep then deletes unless a disk backs onto them.
func (s *Store) ForgetCheckoutDisks(group string) error {
	if !validPreparedKey(group) {
		return fmt.Errorf("vmimage: kept disk group %q must be letters, digits, - or _", group)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.keptDir(), group)); err != nil {
		return fmt.Errorf("vmimage: forget prepared disks of %s: %w", group, err)
	}
	return nil
}

// DropKeptCheckoutDisks stops keeping every prepared disk and deletes those
// no disk backs onto.
func (s *Store) DropKeptCheckoutDisks() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.keptDir()); err != nil {
		return fmt.Errorf("vmimage: drop kept prepared disks: %w", err)
	}
	return s.sweepLocked()
}

// pinPrepared hard-links the prepared disk under key to pin, building the
// disk first when it is missing.
func (s *Store) pinPrepared(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error, pin string) (Base, error) {
	if !validPreparedKey(key) {
		return Base{}, fmt.Errorf("vmimage: prepared disk key %q must be letters, digits, - or _", key)
	}
	base := Base{ID: preparedPrefix + key, Path: filepath.Join(s.preparedDir(), key, "disk.raw")}
	for {
		// Pinned under the store lock as soon as it exists, so a sweep
		// cannot remove it first.
		s.mu.Lock()
		_, statErr := os.Stat(base.Path)
		var err error
		if statErr == nil {
			// Made under the lock: dropping the kept disks removes it.
			if err = os.MkdirAll(filepath.Dir(pin), 0o755); err == nil {
				err = s.pinLocked(base.Path, pin)
			}
		}
		s.mu.Unlock()
		if statErr == nil {
			return base, err
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return Base{}, fmt.Errorf("vmimage: prepared disk %s: %w", key, statErr)
		}
		built := s.builds.DoChan(preparedPrefix+key, func() (any, error) {
			buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
			defer cancel()
			return nil, s.buildPrepared(buildCtx, key, size, fill)
		})
		select {
		case <-ctx.Done():
			return Base{}, fmt.Errorf("vmimage: prepared disk %s: %w", key, ctx.Err())
		case res := <-built:
			if res.Err != nil {
				return Base{}, res.Err
			}
		}
	}
}

func (s *Store) buildPrepared(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error) error {
	dest := filepath.Join(s.preparedDir(), key)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	work, err := os.MkdirTemp(s.tmpDir(), "prepared-")
	if err != nil {
		return fmt.Errorf("vmimage: prepared disk %s: %w", key, err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	stage := filepath.Join(work, "root")
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	if err := fill(ctx, stage); err != nil {
		return fmt.Errorf("vmimage: fill prepared disk %s: %w", key, err)
	}
	disk := filepath.Join(work, "disk.raw")
	if err := mkfs(ctx, disk, size, "aether-checkout", stage); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Chmod(disk, 0o444); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(work, dest); err != nil {
		return fmt.Errorf("vmimage: store prepared disk %s: %w", key, err)
	}
	return nil
}

func validPreparedKey(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
