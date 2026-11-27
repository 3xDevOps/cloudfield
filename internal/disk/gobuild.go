package disk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

// GoBuildInUseAge is how long before now TrimGoBuildCache's callers put its
// cutoff. It must exceed the longest go command by more than Go's hourly
// mtime refresh.
const GoBuildInUseAge = 2 * time.Hour

// TrimGoBuildCache removes the entries of the Go build caches that were last
// used before cutoff, oldest first, until it has freed at least excess bytes.
// An entry spare reports stays; a nil spare reports none. It walks the same
// 256 shard directories as Go's own trim. A cache that cannot be read is
// left alone and reported; the others are still trimmed.
func TrimGoBuildCache(ctx context.Context, caches []*os.Root, cutoff time.Time, excess uint64, spare func(cache *os.Root, name string) bool) error {
	type entry struct {
		cache *os.Root
		name  string
		size  uint64
		used  time.Time
	}
	var entries []entry
	var errs []error
	collect := func(root *os.Root) error {
		shards, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if !shard.IsDir() || len(shard.Name()) != 2 {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			names, err := fs.ReadDir(root.FS(), shard.Name())
			if err != nil {
				return err
			}
			for _, dirEntry := range names {
				name := shard.Name() + "/" + dirEntry.Name()
				if !strings.HasSuffix(name, "-a") && !strings.HasSuffix(name, "-d") {
					continue
				}
				info, err := root.Lstat(name)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if !info.ModTime().Before(cutoff) || spare != nil && spare(root, name) {
					continue
				}
				size := uint64(0)
				if info.Mode().IsRegular() {
					size = uint64(info.Size())
				} else if info.IsDir() {
					_ = fs.WalkDir(root.FS(), name, func(_ string, d fs.DirEntry, err error) error {
						if err == nil && d.Type().IsRegular() {
							if file, err := d.Info(); err == nil {
								size += uint64(file.Size())
							}
						}
						return nil
					})
				}
				entries = append(entries, entry{root, name, size, info.ModTime()})
			}
		}
		return nil
	}
	for _, root := range caches {
		if err := collect(root); err != nil {
			errs = append(errs, fmt.Errorf("disk: trim Go build cache %s: %w", root.Name(), err))
		}
	}
	slices.SortFunc(entries, func(a, b entry) int { return a.used.Compare(b.used) })
	var freed uint64
	for _, e := range entries {
		if freed >= excess {
			break
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		// A go command may have used the entry, refreshing it, since the walk.
		info, err := e.cache.Lstat(e.name)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !info.ModTime().Before(cutoff) {
			continue
		}
		if err == nil {
			err = e.cache.RemoveAll(e.name)
		}
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		freed += e.size
	}
	return errors.Join(errs...)
}
