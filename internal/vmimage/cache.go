package vmimage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
)

const (
	// cacheSize is every cache filesystem's size; it is sparse.
	cacheSize = 32 << 30
	// cacheLimit is the most a pool's newest cache may take on the host; a
	// larger one is dropped, so the pool's next session starts empty.
	cacheLimit = 20 << 30
	// currentCache names, in a pool's directory, the link to its newest
	// cache.
	currentCache = "current"
	// cacheIdleAge is how long a pool's newest cache stays after a session
	// last started from it or a sweep last found a disk reading through it.
	cacheIdleAge = 7 * 24 * time.Hour
)

// NewCacheDisk creates a cache disk at path: a qcow2 disk over pool's newest
// cache, as NewRootDisk makes over a base, or an empty raw one.
func (s *Store) NewCacheDisk(ctx context.Context, pool, path string) (Disk, error) {
	if !validPool(pool) {
		return Disk{}, fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Disk{}, fmt.Errorf("vmimage: cache disk: %w", err)
	}
	// Linked under the lock, so a promotion's sweep cannot delete it first.
	s.mu.Lock()
	newest, err := s.readNewest(filepath.Join(s.cachesDir(), pool))
	if err == nil {
		err = s.pinLocked(newest.Path, rootPin(path, newest.ID))
	}
	if err == nil {
		now := time.Now()
		err = os.Chtimes(newest.Path, now, now)
	}
	s.mu.Unlock()
	if err == nil {
		disk, rootErr := s.NewRootDisk(ctx, newest, path)
		if rootErr != nil {
			return Disk{}, errors.Join(rootErr, removeIfExists(rootPin(path, newest.ID)))
		}
		return disk, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Disk{}, err
	}
	tmp := path + ".tmp"
	if err = removeIfExists(tmp); err != nil {
		return Disk{}, err
	}
	if err = mkfs(ctx, tmp, cacheSize, "aether-cache", ""); err != nil {
		return Disk{}, errors.Join(err, removeIfExists(tmp))
	}
	if err = os.Rename(tmp, path); err != nil {
		return Disk{}, fmt.Errorf("vmimage: cache disk %s: %w", path, err)
	}
	return Disk{Path: path, Format: FormatRaw}, nil
}

func validPool(pool string) bool {
	return pool != "" && filepath.Base(pool) == pool && !strings.HasPrefix(pool, ".")
}

func (s *Store) readNewest(dir string) (Base, error) {
	name, err := os.Readlink(filepath.Join(dir, currentCache))
	if err != nil {
		return Base{}, err
	}
	return Base{ID: "cache:" + strings.TrimSuffix(name, ".raw"), Path: filepath.Join(dir, name)}, nil
}

// publishCacheLocked moves disk, a session's cache as of seq, into dir as
// the pool's newest cache.
func (s *Store) publishCacheLocked(dir, disk string, seq int64) (Base, error) {
	id, err := newLocalID()
	if err != nil {
		return Base{}, err
	}
	name := fmt.Sprintf("%020d-%s.raw", seq, strings.TrimPrefix(id, localPrefix))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache pool: %w", err)
	}
	if err = os.Chmod(disk, 0o444); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	if err = os.Rename(disk, filepath.Join(dir, name)); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	if err = linkNewest(dir, name); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	return Base{ID: "cache:" + strings.TrimSuffix(name, ".raw"), Path: filepath.Join(dir, name)}, nil
}

func linkNewest(dir, name string) error {
	link := filepath.Join(dir, currentCache)
	if err := removeIfExists(link + ".tmp"); err != nil {
		return err
	}
	if err := os.Symlink(name, link+".tmp"); err != nil {
		return err
	}
	return os.Rename(link+".tmp", link)
}

// SnapshotCache links a stopped VM's cache disk, and a qcow2 one's base,
// into the store for PromoteCache, so the VM may be deleted meanwhile. A VM
// that starts again first takes a disk of its own with UnshareCache.
func (s *Store) SnapshotCache(disk Disk) (string, error) {
	work, err := os.MkdirTemp(s.tmpDir(), fmt.Sprintf("snapshot-%d-", time.Now().UnixNano()))
	if err != nil {
		return "", fmt.Errorf("vmimage: snapshot cache: %w", err)
	}
	switch {
	case disk.Format == FormatRaw:
	case disk.Format == FormatQcow2 && disk.Backing != "":
		err = s.pin(disk.Backing, filepath.Join(work, "base.raw"))
	default:
		err = fmt.Errorf("a cache disk must be raw or a qcow2 disk over a base, not %+v", disk)
	}
	if err == nil {
		err = s.pin(disk.Path, filepath.Join(work, "cache"))
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("vmimage: snapshot cache %s: %w", disk.Path, err), os.RemoveAll(work))
	}
	return work, nil
}

// UnshareCache gives the cache disk of a VM about to start a file of its
// own while a promotion still reads the one it has.
func (s *Store) UnshareCache(ctx context.Context, disk Disk) error {
	linked, err := linkedElsewhere(disk.Path)
	if err != nil || !linked {
		return err
	}
	tmp := disk.Path + ".tmp"
	if err = removeIfExists(tmp); err != nil {
		return err
	}
	if err = copyDisk(ctx, disk.Path, tmp); err == nil {
		err = os.Rename(tmp, disk.Path)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("vmimage: unshare cache %s: %w", disk.Path, err), removeIfExists(tmp))
	}
	return nil
}

// reservePromotion holds, until release, the room a promotion of cache may
// take on the store's filesystem: a copy of cache, and for one read over
// base a copy of base and cache's blocks again, which land in that copy's
// holes. It refuses a promotion that would leave less than the reserve
// free, counting what the promotions under way hold.
func (s *Store) reservePromotion(cache, base string) (release func(), err error) {
	if s.minFree < 0 {
		return func() {}, nil
	}
	info, err := os.Stat(cache)
	if err != nil {
		return nil, err
	}
	need := uint64(info.Sys().(*syscall.Stat_t).Blocks) * 512
	if info, err = os.Stat(base); err == nil {
		need = 2*need + uint64(info.Sys().(*syscall.Stat_t).Blocks)*512
	}
	data, err := disk.Filesystem(s.dir)
	if err != nil {
		return nil, err
	}
	reserve := disk.Reserve(data.TotalBytes, s.minFree)
	s.mu.Lock()
	defer s.mu.Unlock()
	if data.FreeBytes < reserve+s.promoting+need {
		return nil, fmt.Errorf("skipped: it may write %d bytes and other promotions %d more, and %s has %d free with %d to keep free",
			need, s.promoting, s.dir, data.FreeBytes, reserve)
	}
	s.promoting += need
	return func() {
		s.mu.Lock()
		s.promoting -= need
		s.mu.Unlock()
	}, nil
}

// PromoteCache writes a snapshot SnapshotCache took as a raw disk, a copy
// of a qcow2 one read over its base, and makes that pool's newest cache, or
// drops the pool's caches when it takes more than cacheLimit, then deletes
// the snapshot and every cache of the pool no disk uses. A snapshot older
// than the pool's newest cache, whose promotion finished first, changes
// nothing, and so does one the store's filesystem has no room to write.
func (s *Store) PromoteCache(pool, snapshot string) error {
	defer func() { _ = os.RemoveAll(snapshot) }()
	if !validPool(pool) {
		return fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	disk, cache, base := filepath.Join(snapshot, "disk.raw"), filepath.Join(snapshot, "cache"), filepath.Join(snapshot, "base.raw")
	release, err := s.reservePromotion(cache, base)
	if err != nil {
		return fmt.Errorf("vmimage: promote cache: %w", err)
	}
	defer release()
	if _, statErr := os.Stat(base); statErr == nil {
		top := filepath.Join(snapshot, "top.qcow2")
		if err = copyDisk(ctx, cache, top); err == nil {
			err = flatten(ctx, top, base, disk)
		}
	} else {
		err = copyDisk(ctx, cache, disk)
	}
	if err != nil {
		return fmt.Errorf("vmimage: promote cache: %w", err)
	}
	info, err := os.Stat(disk)
	if err != nil {
		return fmt.Errorf("vmimage: promote cache: %w", err)
	}
	seq := snapshotSeq(snapshot)
	dir := filepath.Join(s.cachesDir(), pool)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch newest, _ := s.readNewest(dir); {
	case newestSeq(newest) > seq:
	case info.Sys().(*syscall.Stat_t).Blocks*512 > cacheLimit:
		err = removeIfExists(filepath.Join(dir, currentCache))
	default:
		_, err = s.publishCacheLocked(dir, disk, seq)
	}
	// The snapshot's link would keep the base it read through from the sweep.
	return errors.Join(err, os.RemoveAll(snapshot), s.sweepCachesLocked(dir))
}

// sweepCachesLocked deletes a pool's caches that no disk links to, but its
// newest only once it has been idle for cacheIdleAge. The newest's
// modification time is when it was last in use.
func (s *Store) sweepCachesLocked(dir string) error {
	newest, _ := os.Readlink(filepath.Join(dir, currentCache))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vmimage: sweep caches: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".raw") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		linked, err := linkedElsewhere(path)
		switch {
		case err != nil:
		case linked && e.Name() == newest:
			now := time.Now()
			err = os.Chtimes(path, now, now)
		case linked:
		case e.Name() != newest:
			err = removeIfExists(path)
		default:
			var info os.FileInfo
			if info, err = os.Stat(path); err == nil && time.Since(info.ModTime()) >= cacheIdleAge {
				err = errors.Join(removeIfExists(filepath.Join(dir, currentCache)), removeIfExists(path))
			}
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// snapshotSeq is when SnapshotCache took a snapshot, from its name.
func snapshotSeq(snapshot string) int64 {
	digits, _, _ := strings.Cut(strings.TrimPrefix(filepath.Base(snapshot), "snapshot-"), "-")
	seq, _ := strconv.ParseInt(digits, 10, 64)
	return seq
}

// newestSeq is when the snapshot a pool's newest cache came from was taken,
// 0 for none.
func newestSeq(newest Base) int64 {
	digits, _, _ := strings.Cut(filepath.Base(newest.Path), "-")
	seq, _ := strconv.ParseInt(digits, 10, 64)
	return seq
}

func (s *Store) cachesDir() string { return filepath.Join(s.dir, "caches") }
