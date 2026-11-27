package helper

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
)

// TestCacheTrimLeavesWhatCameWithTheCache trims a Go build cache as the
// helper does: of two entries last used five hours ago, the one written
// before boot stays, as does an entry used an hour ago.
func TestCacheTrimLeavesWhatCameWithTheCache(t *testing.T) {
	cache, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cache.Close() }()
	if err = cache.Mkdir("ab", 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, age time.Duration) {
		t.Helper()
		used := time.Now().Add(-age)
		if err = cache.WriteFile(name, []byte("entry"), 0o600); err == nil {
			err = cache.Chtimes(name, used, used)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	write("ab/base-d", 5*time.Hour)
	// A file's creation time comes from a clock that runs up to a tick late.
	time.Sleep(50 * time.Millisecond)
	boot := time.Now()
	time.Sleep(50 * time.Millisecond)
	write("ab/own-d", 5*time.Hour)
	write("ab/in-use-a", time.Hour)
	if err = disk.TrimGoBuildCache(t.Context(), []*os.Root{cache}, time.Now().Add(-disk.GoBuildInUseAge), 1<<30, bornBefore(boot)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(cache.Name(), "ab"))
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{"base-d", "in-use-a"}; !slices.Equal(left, want) {
		t.Errorf("after the trim the cache holds %v, want %v", left, want)
	}
}
