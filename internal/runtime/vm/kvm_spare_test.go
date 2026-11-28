//go:build kvm

package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMASpareVMPublishesItsCacheOnlyOncePublished boots a VM whose cache
// disk is unpublished, as a spare session's is, has it write to the cache
// and stops it: the member's pool is left as it was. A second such VM whose
// flag was lifted, as a launch's claim lifts it, publishes its cache when it
// stops, and the next session reads what it wrote.
func TestKVMASpareVMPublishesItsCacheOnlyOncePublished(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-spare")
	if err := os.MkdirAll(filepath.Join(src, "pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	pool := runtime.Mount{HostPath: filepath.Join(src, "pool"), ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	poolDir := filepath.Join(e.root, imagesDir, "caches", cachePool(pool))
	t.Cleanup(func() { _ = os.RemoveAll(poolDir) })
	newest := func() string {
		name, _ := os.Readlink(filepath.Join(poolDir, "current"))
		return name
	}
	spare := func(key string) {
		t.Helper()
		t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20, Mounts: []runtime.Mount{pool},
			CacheUnpublished: true,
		}
		if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
			t.Fatal(err)
		}
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}

	spare("run-spare-kept")
	if held, err := e.unitMemory("run-spare-kept"); err == nil {
		t.Logf("a started session VM with no agent holds %d MiB of the host", held>>20)
	}
	vmRun(t, e, "run-spare-kept", "echo spare > /aether-cache/marker && sync")
	before := newest()
	if err := e.stop(t.Context(), "run-spare-kept", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	e.promotions.Wait()
	if got := newest(); got != before {
		t.Fatalf("a stopped spare VM changed the pool's newest cache from %q to %q", before, got)
	}

	spare("run-spare-claimed")
	vmRun(t, e, "run-spare-claimed", "echo claimed > /aether-cache/marker && sync")
	if err := e.publishCache("run-spare-claimed"); err != nil {
		t.Fatal(err)
	}
	if err := e.stop(t.Context(), "run-spare-claimed", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	e.promotions.Wait()
	if got := newest(); got == before {
		t.Fatal("a claimed session's VM did not publish its cache when it stopped")
	}
	rootSession(t, e, "run-spare-next", "docker.io/library/ubuntu:24.04", pool)
	if out, _ := vmRun(t, e, "run-spare-next", "cat /aether-cache/marker"); !strings.Contains(out, "claimed") {
		t.Fatalf("the next session's cache holds %q, want what the claimed session wrote", out)
	}
}
