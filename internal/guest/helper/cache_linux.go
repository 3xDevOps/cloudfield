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

// trimCacheDisk keeps the cache disk mounted at root from filling: every
// cacheTrimInterval, while the disk is more than half full, it deletes the
// least recently used Go build cache entries until it is back to half. go
// test, go vet and a module download fail on a full disk, and Go's own trim
// goes by age alone.
func trimCacheDisk(root string) {
	for range time.Tick(cacheTrimInterval) {
		if err := trimCacheOnce(root); err != nil {
			slog.Warn("guest: trim the Go build cache", "error", err)
		}
	}
}

func trimCacheOnce(root string) error {
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
	return disk.TrimGoBuildCache(context.Background(), []*os.Root{cache}, time.Now().Add(-disk.GoBuildInUseAge), use.UsedBytes-use.TotalBytes/2)
}
