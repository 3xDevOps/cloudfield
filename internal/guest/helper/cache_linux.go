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

const (
	cacheTrimInterval = 10 * time.Minute
	// Must exceed the longest go command by more than Go's hourly mtime refresh.
	goBuildInUseAge = 2 * time.Hour
)

// trimCacheDisk keeps the cache disk mounted at root at most half full,
// from boot and every cacheTrimInterval: go test, go vet and a module
// download fail on a full one, and Go's own trim goes by age alone.
func trimCacheDisk(root string) {
	for {
		if err := trimCacheOnce(root); err != nil {
			slog.Warn("guest: trim the Go build cache", "error", err)
		}
		time.Sleep(cacheTrimInterval)
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
	return disk.TrimGoBuildCache(context.Background(), []*os.Root{cache}, time.Now().Add(-goBuildInUseAge), use.UsedBytes-use.TotalBytes/2)
}
