package vmimage

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCachePromotionKeepsTheReserveFree(t *testing.T) {
	needTools(t, "mkfs.ext4", "cp", "qemu-img", "qemu-io")
	s := openStore(t, nil)
	dir := t.TempDir()
	pool := filepath.Join(s.cachesDir(), "pool1")
	first, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(dir, "first.img"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotCache(first)
	if err == nil {
		err = s.PromoteCache("pool1", snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	newest, err := s.readNewest(pool)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(dir, "session.img"))
	if err != nil {
		t.Fatal(err)
	}
	qemuWrite(t, session.Path, 0xcd)
	want, err := os.ReadFile(session.Path)
	if err != nil {
		t.Fatal(err)
	}

	// A reserve no filesystem meets stands in for a full one.
	s.minFree = math.MaxInt64
	for _, cache := range []Disk{session, first} {
		if snapshot, err = s.SnapshotCache(cache); err != nil {
			t.Fatal(err)
		}
		if err = s.PromoteCache("pool1", snapshot); err == nil || !strings.Contains(err.Error(), "skipped") {
			t.Errorf("promoting the %s cache without room = %v, want it skipped", cache.Format, err)
		}
	}
	if after, _ := s.readNewest(pool); after.Path != newest.Path {
		t.Errorf("the pool's newest cache is %q after a skipped promotion, want %q", after.Path, newest.Path)
	}
	if got, _ := os.ReadFile(session.Path); !bytes.Equal(got, want) {
		t.Error("a skipped promotion changed the session's cache disk")
	}
	if left, _ := os.ReadDir(s.tmpDir()); len(left) != 0 {
		t.Errorf("a skipped promotion left %d entries in the store's work directory", len(left))
	}

	s.minFree = 1
	if snapshot, err = s.SnapshotCache(session); err == nil {
		err = s.PromoteCache("pool1", snapshot)
	}
	if err != nil {
		t.Fatalf("promoting with room: %v", err)
	}
	if after, _ := s.readNewest(pool); after.Path == newest.Path {
		t.Error("a promotion with room did not replace the pool's newest cache")
	}
	if s.promoting != 0 {
		t.Errorf("%d bytes stay held after every promotion ended", s.promoting)
	}
}
