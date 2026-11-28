package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/guest"
	"github.com/3xDevOps/cloudfield/internal/guest/home"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/vmimage"
)

// checkouts lists a spec's checkout and extra worktrees, in Docker's order.
func checkouts(spec runtime.Spec) []runtime.Mount {
	var out []runtime.Mount
	if spec.WorktreeHostPath != "" {
		out = append(out, runtime.Mount{HostPath: spec.WorktreeHostPath, ContainerPath: spec.WorktreeMountPath})
	}
	return append(out, spec.ExtraWorktrees...)
}

func (e *engine) plan(rec *record) (plan, error) {
	return planMounts(rec.Checkouts, rec.Mounts, rec.NestedDocker, e.dir(rec.Key), rootOnly)
}

// homeConfig serves every file as user's, "uid:gid", the session user, or
// root for an empty user.
func (p plan) homeConfig(user string) *home.Config {
	if len(p.home) == 0 {
		return nil
	}
	uid, gid, _ := strings.Cut(user, ":")
	u, _ := strconv.ParseUint(uid, 10, 32)
	g, _ := strconv.ParseUint(gid, 10, 32)
	return &home.Config{Shares: p.home, UID: uint32(u), GID: uint32(g)}
}

func cacheMount(mounts []runtime.Mount) (runtime.Mount, bool) {
	for _, m := range mounts {
		if m.Kind == runtime.MountCache {
			return m, true
		}
	}
	return runtime.Mount{}, false
}

// cachePool names the member's pool of caches by the host directory a
// container would bind for it.
func cachePool(m runtime.Mount) string {
	sum := sha256.Sum256([]byte(m.HostPath))
	return hex.EncodeToString(sum[:8])
}

// retireCache takes the cache disk from a VM that stopped for good, by a
// stop or a deletion while stopped or saved: its next cold start makes one
// over its pool's newest cache. A disk that holds the guest's last writes
// and more than it started with becomes that newest first, so the member's
// next session starts from it: it is linked under the key's lock, then
// copied and published in the background. Counts the guest did not give
// read as more. A save alone never retires, and an Isolated session's
// cache, an overlay in a container, never goes back.
func (e *engine) retireCache(rec *record) {
	added := rec.CacheUse != rec.CacheSeed || rec.CacheUse == guest.CacheUse{}
	if m, ok := cacheMount(rec.Mounts); ok && rec.CacheRan && rec.CacheSynced && added && m.Overlay == "" {
		snapshot, err := e.cfg.Images.SnapshotCache(rec.Cache)
		if err != nil {
			slog.Warn("vm runtime: link the cache of a stopped VM", "vm", rec.Key, "error", err)
		} else {
			e.promotions.Go(func() {
				if err := e.cfg.Images.PromoteCache(cachePool(m), snapshot); err != nil {
					slog.Warn("vm runtime: promote the cache of a stopped VM", "vm", rec.Key, "error", err)
				}
			})
		}
	}
	e.dropCache(rec)
}

// cacheUse asks the guest what its cache disk has free: zeros when it does
// not answer, as a helper from before the call does not.
func (e *engine) cacheUse(ctx context.Context, key string) guest.CacheUse {
	h, err := e.helper(key)
	var use guest.CacheUse
	if err == nil {
		callCtx, cancel := context.WithTimeout(ctx, helperCallTimeout)
		use, err = h.CacheUse(callCtx)
		cancel()
	}
	if err != nil {
		slog.Warn("vm runtime: count what the guest's cache disk holds; its cache is promoted as if it had grown", "vm", key, "error", err)
	}
	return use
}

// dropCache removes a stopped VM's cache disk. The record lets go of the
// disk before the files do, so a server that stops in between leaves files
// the next one removes.
func (e *engine) dropCache(rec *record) {
	if _, ok := cacheMount(rec.Mounts); !ok {
		return
	}
	if rec.Cache.Path != "" {
		rec.Cache, rec.CacheRan, rec.CacheSeed = vmimage.Disk{}, false, guest.CacheUse{}
		if err := e.store(rec); err != nil {
			slog.Warn("vm runtime: record the removal of a stopped VM's cache disk", "vm", rec.Key, "error", err)
			return
		}
	}
	if err := vmimage.DropCacheDisk(e.path(rec.Key, cacheDisk)); err != nil {
		slog.Warn("vm runtime: remove the cache disk of a stopped VM", "vm", rec.Key, "error", err)
	}
}
