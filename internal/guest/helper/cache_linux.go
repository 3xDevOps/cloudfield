package helper

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/rootfs"
)

const cacheTrimInterval = 10 * time.Minute

// trimCacheDisk bounds the Go build cache this boot adds to the cache disk
// mounted at root: every cacheTrimInterval, while the disk is more than half
// full, it deletes the least recently used entries written since boot. go
// test, go vet and a module download fail on a full disk, and Go's own trim
// goes by age alone.
func trimCacheDisk(root string) {
	boot := time.Now()
	for range time.Tick(cacheTrimInterval) {
		if err := trimCacheOnce(root, boot); err != nil {
			slog.Warn("guest: trim the Go build cache", "error", err)
		}
	}
}

func trimCacheOnce(root string, boot time.Time) error {
	use, err := disk.Filesystem(root)
	if err != nil || use.UsedBytes <= use.TotalBytes/2 {
		return err
	}
	volume, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = volume.Close() }()
	// The session's users write the volume, so go-build may be their symlink.
	cache, err := rootfs.OpenRoot(volume, "go-build")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = cache.Close() }()
	return disk.TrimGoBuildCache(context.Background(), []*os.Root{cache}, time.Now().Add(-disk.GoBuildInUseAge), use.UsedBytes-use.TotalBytes/2, bornBefore(boot))
}

// bornBefore reports the entries created before t, and those whose creation
// time cannot be read. The disk only reads those through the member's
// newest cache: deleting one frees nothing on the host, and its discard
// copies the clusters it shares into the disk's own layer. The use time
// cannot tell them apart, because Go moves it on an entry a build reads.
func bornBefore(t time.Time) func(cache *os.Root, name string) bool {
	return func(cache *os.Root, name string) bool {
		entry, err := cache.OpenFile(name, unix.O_PATH|unix.O_NOFOLLOW, 0)
		if err != nil {
			return true
		}
		defer func() { _ = entry.Close() }()
		var st unix.Statx_t
		err = unix.Statx(int(entry.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_BTIME, &st)
		return err != nil || st.Mask&unix.STATX_BTIME == 0 || time.Unix(st.Btime.Sec, int64(st.Btime.Nsec)).Before(t)
	}
}
