package vmimage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPreparedDiskLeavesItsJournalUnallocated(t *testing.T) {
	needTools(t, "mkfs.ext4", "qemu-img")
	s := openStore(t, nil)
	fill := func(_ context.Context, dir string) error {
		return os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	}
	if err := s.NewCheckoutDisk(t.Context(), "k", 128<<30, fill, filepath.Join(t.TempDir(), "disk.qcow2")); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(s.preparedDir(), "k", "disk.raw"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Blocks*512 > 32<<20 {
		t.Errorf("a 128 GiB prepared disk takes %d MiB on the host, want its 1 GiB journal unallocated", st.Blocks*512>>20)
	}
}

func TestKeptPreparedDiskOutlivesTheSweepUntilAnotherTakesItsSlot(t *testing.T) {
	needTools(t, "mkfs.ext4")
	s := openStore(t, nil)
	fill := func(_ context.Context, dir string) error {
		return os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	}
	exists := func(key string) bool {
		_, err := os.Stat(filepath.Join(s.preparedDir(), key, "disk.raw"))
		return err == nil
	}
	keep := func(key string) {
		t.Helper()
		if err := s.KeepCheckoutDisk(t.Context(), "ws", "1000-1000", key, 64<<20, fill); err != nil {
			t.Fatal(err)
		}
		if err := s.Sweep(); err != nil {
			t.Fatal(err)
		}
	}
	keep("old")
	if slots, err := s.KeptCheckoutDisks("ws"); err != nil || len(slots) != 1 || slots[0] != "1000-1000" {
		t.Fatalf("kept slots = %v, %v", slots, err)
	}
	if !exists("old") {
		t.Fatal("the sweep deleted a kept prepared disk no session uses")
	}
	keep("new")
	if exists("old") || !exists("new") {
		t.Fatalf("after another disk took the slot: old kept %v, new kept %v", exists("old"), exists("new"))
	}
	if err := s.DropKeptCheckoutDisks(); err != nil {
		t.Fatal(err)
	}
	if slots, err := s.KeptCheckoutDisks("ws"); err != nil || len(slots) != 0 || exists("new") {
		t.Fatalf("after dropping: slots %v, %v, disk kept %v", slots, err, exists("new"))
	}
	keep("new")
	if !exists("new") {
		t.Fatal("a disk kept after a drop did not survive the sweep")
	}
	if err := s.ForgetCheckoutDisks("ws"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if slots, err := s.KeptCheckoutDisks("ws"); err != nil || len(slots) != 0 || exists("new") {
		t.Fatalf("after forgetting: slots %v, %v, disk kept %v", slots, err, exists("new"))
	}
}
