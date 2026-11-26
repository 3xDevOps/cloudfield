package helper

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/sysstats"
)

const (
	trimInterval = 10 * time.Second
	// trimKeep is the inactive page cache a trim leaves alone.
	trimKeep = 64 << 20
	// trimStall is the share of the last minute, in percent, the cgroup's
	// tasks may have stalled on memory before a trim holds off: reading back
	// a page that was in regular use before the trim dropped it counts as
	// such a stall.
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
	// The kernel answers EAGAIN when it freed less than asked and EINTR
	// when a signal cut it short. An os.File would wait on the poller for a
	// cgroup file to turn writable again, which it never does.
	fd, err := unix.Open(filepath.Join(dir, "memory.reclaim"), unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open memory.reclaim: %w", err)
	}
	_, err = unix.Write(fd, fmt.Appendf(nil, "%d swappiness=0", (cold-trimKeep)/4))
	_ = unix.Close(fd)
	if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
		return fmt.Errorf("write memory.reclaim: %w", err)
	}
	return nil
}
