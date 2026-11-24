package helper

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/sysstats"
)

const (
	trimInterval = 10 * time.Second
	// trimKeep is the inactive page cache a trim leaves alone.
	trimKeep = 64 << 20
	// trimStall is the share of the last minute, in percent, the cgroup's
	// tasks may have stalled on memory before a trim holds off: reading back
	// a file the trim dropped counts as such a stall.
	trimStall = 1.0
)

// trimPageCache has the kernel free a quarter of the page cache it holds
// inactive in the cgroup dir every trimInterval, so free page reporting
// returns it to the host; docs/vm-guest.md#memory-the-guest-returns says why.
func trimPageCache(dir string) {
	for range time.Tick(trimInterval) {
		if err := trimOnce(dir); err != nil {
			slog.Warn("guest: trim the page cache", "error", err)
		}
	}
}

func trimOnce(dir string) error {
	stall, err := sysstats.CgroupMemoryPressure(dir)
	if err != nil {
		return err
	}
	if stall != nil && *stall >= trimStall {
		return nil
	}
	stat, err := readCounters(filepath.Join(dir, "memory.stat"), 1)
	if err != nil {
		return err
	}
	cold := stat["inactive_file"]
	if cold <= trimKeep {
		return nil
	}
	// The kernel answers EAGAIN when it freed less than asked.
	err = os.WriteFile(filepath.Join(dir, "memory.reclaim"), fmt.Appendf(nil, "%d swappiness=0", (cold-trimKeep)/4), 0)
	if errors.Is(err, unix.EAGAIN) {
		return nil
	}
	return err
}
