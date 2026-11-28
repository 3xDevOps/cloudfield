package vmimage

import (
	"bytes"
	"errors"
	"io/fs"
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

	empty, err := s.NewCacheDisk(t.Context(), "pool2", filepath.Join(dir, "empty.img"))
	if err != nil {
		t.Fatal(err)
	}

	// A reserve no filesystem meets stands in for a full one.
	s.minFree = math.MaxInt64
	for name, cache := range map[string]Disk{"pool1": session, "pool2": empty} {
		if snapshot, err = s.SnapshotCache(cache); err != nil {
			t.Fatal(err)
		}
		if err = s.PromoteCache(name, snapshot); err == nil || !strings.Contains(err.Error(), "skipped") {
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

	release, err := s.reservePromotion(first.Path, filepath.Join(dir, "no-base"))
	if err != nil {
		t.Fatalf("holding room for a promotion: %v", err)
	}
	if s.promoting == 0 {
		t.Error("a promotion under way holds no room against the next one")
	}
	release()
	last, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(dir, "last.img"))
	if err != nil {
		t.Fatal(err)
	}
	s.promoting = math.MaxInt64
	if snapshot, err = s.SnapshotCache(last); err != nil {
		t.Fatal(err)
	}
	if err = s.PromoteCache("pool1", snapshot); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("promoting while other promotions hold the room = %v, want it skipped", err)
	}
	s.promoting = 0

	// The earlier session is deleted: no disk links the cache it read
	// over, which is no longer the pool's newest.
	s.minFree = math.MaxInt64
	if snapshot, err = s.SnapshotCache(last); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(os.Remove(session.Path), os.Remove(session.Backing)); err != nil {
		t.Fatal(err)
	}
	if err = s.PromoteCache("pool1", snapshot); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("promoting a cache without room = %v, want it skipped", err)
	}
	if _, err = os.Stat(newest.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a skipped promotion left the cache no disk reads on disk: stat = %v", err)
	}
}
