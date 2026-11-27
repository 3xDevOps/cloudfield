package helper

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/rootfs"
)

const cacheTrimInterval = 10 * time.Minute

// trimCacheDisk keeps the cache disk mounted at root at most half full,
// from boot and every cacheTrimInterval: go test, go vet and a module
// download fail on a full one, and Go's own trim goes by age alone.
func trimCacheDisk(root string) {
	// seen holds what the clock read at each of the last passes, so the
	// cutoff is disk.GoBuildInUseAge of running time ago. A saved VM does
	// not run, and its clock is set forward when it wakes, which ages the
	// entries of a go command that runs across the save.
	var seen [disk.GoBuildInUseAge / cacheTrimInterval]time.Time
	for i := range seen {
		seen[i] = time.Now().Add(time.Duration(i)*cacheTrimInterval - disk.GoBuildInUseAge)
	}
	for i := 0; ; i++ {
		cutoff := seen[i%len(seen)]
		seen[i%len(seen)] = time.Now()
		if err := trimCacheOnce(root, cutoff); err != nil {
			slog.Warn("guest: trim the Go build cache", "error", err)
		}
		time.Sleep(cacheTrimInterval)
	}
}

func trimCacheOnce(root string, cutoff time.Time) error {
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
	return disk.TrimGoBuildCache(context.Background(), []*os.Root{cache}, cutoff, use.UsedBytes-use.TotalBytes/2)
}
