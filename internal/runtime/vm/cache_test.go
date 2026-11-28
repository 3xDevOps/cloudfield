package vm

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// A stopped VM holds no cache disk and no link to the cache it was made
// over, and takes a disk over the member's newest cache when it starts
// again. A saved VM, whose memory holds the disk's filesystem, keeps its
// own. A server start removes the cache disks an earlier server left with
// stopped VMs, and promotes none of them.
func TestAStoppedVMKeepsNoCacheDisk(t *testing.T) {
	h := newHarness(t)
	cache := runtime.Mount{HostPath: "/data/cache/m1/runs", ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	session := func(key string) {
		spec := sessionSpec(key)
		spec.Mounts = []runtime.Mount{cache}
		h.createStarted(key, spec)
	}
	held := func(key string) []string {
		files, err := filepath.Glob(h.engine.path(key, cacheDisk+"*"))
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	made := func() int {
		h.images.mu.Lock()
		defer h.images.mu.Unlock()
		return h.images.cacheDisks
	}

	session("run-a")
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if got := held("run-a"); len(got) != 2 {
		t.Fatalf("a saved VM holds %v, want its cache disk and the link to its base", got)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := held("run-a"); len(got) != 0 {
		t.Errorf("a stopped VM still holds %v", got)
	}
	if rec, err := h.engine.load("run-a"); err != nil || rec.Cache.Path != "" {
		t.Errorf("a stopped VM's record names cache disk %+v, %v", rec.Cache, err)
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(h.units.last().launch.Args, " "); made() != 2 || len(held("run-a")) != 2 || !strings.Contains(args, "cache.img,image_type=qcow2,backing_files=on,id=cache,serial=cache") {
		t.Errorf("a VM started again had %d cache disks made, holds %v and launches with:\n%s\nwant a second cache disk, attached", made(), held("run-a"), args)
	}

	// A guest the stop had to kill promotes nothing, and stopped all the same.
	session("run-k")
	h.guest("run-k").unresponsive = true
	if err := h.engine.stop(t.Context(), "run-k", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := held("run-k"); len(got) != 0 {
		t.Errorf("a VM its stop had to kill still holds %v", got)
	}

	// What an earlier server left: run-old stopped with its cache disk,
	// still to promote after a promotion that failed, run-cut stopped with
	// the removal recorded but not done, run-saved saved.
	for _, key := range []string{"run-old", "run-cut"} {
		session(key)
		rec, err := h.engine.load(key)
		if err != nil {
			t.Fatal(err)
		}
		disk := rec.Cache
		if err = h.engine.stop(t.Context(), key, time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err = h.images.NewCacheDisk(t.Context(), "", disk.Path); err != nil {
			t.Fatal(err)
		}
		if key == "run-cut" {
			continue
		}
		if rec, err = h.engine.load(key); err != nil {
			t.Fatal(err)
		}
		rec.Cache, rec.CacheRan = disk, true
		if !rec.CacheSynced {
			t.Fatal("a clean stop left the cache unsynced")
		}
		if err = h.engine.store(rec); err != nil {
			t.Fatal(err)
		}
	}
	session("run-saved")
	if err := h.engine.save(t.Context(), "run-saved"); err != nil {
		t.Fatal(err)
	}
	h.engine.promotions.Wait()
	h.images.mu.Lock()
	h.images.promoted = make(chan string, 1)
	h.images.mu.Unlock()
	next := h.newEngine(filepath.Dir(h.engine.root))
	next.settle(t.Context())
	next.promotions.Wait()
	select {
	case got := <-h.images.promoted:
		t.Errorf("a server start promoted %q", got)
	default:
	}
	for _, key := range []string{"run-old", "run-cut"} {
		if rec, err := next.load(key); err != nil || rec.Cache.Path != "" || len(held(key)) != 0 {
			t.Errorf("after a server start, stopped %s has cache disk %+v, %v and holds %v", key, rec.Cache, err, held(key))
		}
	}
	for _, key := range []string{"run-saved", "run-a"} {
		if got := held(key); len(got) != 2 {
			t.Errorf("after a server start, %s, which did not stop, holds %v; want its cache disk and the link to its base", key, got)
		}
	}
	if err := next.restore(t.Context(), "run-saved"); err != nil {
		t.Errorf("restore a saved VM after a server start: %v", err)
	}
}
