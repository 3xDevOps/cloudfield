package scheduler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// oomWatcher tracks each live container's cgroup v2 oom_kill counter. The
// first reading is the baseline; a later increase is reported, and killedAt
// keeps when.
type oomWatcher struct {
	mu       sync.Mutex
	checked  bool
	enabled  bool
	cgroup   string
	proc     string
	paths    map[runtime.ID]string
	kills    map[runtime.ID]uint64
	killedAt map[runtime.ID]time.Time
}

type memoryStats struct {
	oomKills uint64
	peak     uint64
	max      uint64
}

func (w *oomWatcher) enable() bool {
	if !w.checked {
		w.checked = true
		w.cgroup, w.proc = "/sys/fs/cgroup", "/proc"
		_, err := os.Stat(filepath.Join(w.cgroup, "cgroup.controllers"))
		w.enabled = err == nil
		w.paths = map[runtime.ID]string{}
		w.kills = map[runtime.ID]uint64{}
		w.killedAt = map[runtime.ID]time.Time{}
	}
	return w.enabled
}

// oomProbeTimeout bounds one container's Docker lookup so a stalled reply
// cannot block the sweep loop.
const oomProbeTimeout = 5 * time.Second

type oomTarget struct {
	container runtime.ID
	workspace domain.WorkspaceID
	run       domain.RunID
}

func (s *Scheduler) checkOOMKills(ctx context.Context) {
	pids, ok := s.cfg.Runtime.(runtime.InitPIDRuntime)
	s.oom.mu.Lock()
	defer s.oom.mu.Unlock()
	if !ok || !s.oom.enable() {
		return
	}
	s.mu.Lock()
	var targets []oomTarget
	for _, e := range s.runs {
		if !e.finalizing && e.containerID != "" {
			targets = append(targets, oomTarget{e.containerID, e.workspaceID, e.runID})
		}
	}
	s.mu.Unlock()

	seen := map[runtime.ID]bool{}
	for _, t := range targets {
		seen[t.container] = true
		s.checkOOMKill(ctx, pids, t)
	}
	for id := range s.oom.kills {
		if !seen[id] {
			s.oom.forget(id)
		}
	}
}

// checkOOMKill reads t's counter and notes an increase on its timeline. The
// caller holds s.oom.mu.
func (s *Scheduler) checkOOMKill(ctx context.Context, pids runtime.InitPIDRuntime, t oomTarget) memoryStats {
	probeCtx, cancel := context.WithTimeout(ctx, oomProbeTimeout)
	stats, err := s.oom.read(probeCtx, pids, t.container)
	cancel()
	if err != nil {
		s.oom.forget(t.container)
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, runtime.ErrNotFound) {
			slog.Warn("scheduler: read container memory events", "run", t.run, "container", t.container, "error", err)
		}
		return stats
	}
	prev, known := s.oom.kills[t.container]
	s.oom.kills[t.container] = stats.oomKills
	if known && stats.oomKills > prev {
		s.oom.killedAt[t.container] = time.Now()
		s.publishTimeline(ctx, t.workspace, t.run, "", events.TimelineNote, oomMessage(stats))
	}
	return stats
}

// oomKillWindow is how long after the kernel killed a process in a session
// for memory the death of its agent is put down to that.
const oomKillWindow = 2 * time.Minute

// recentOOMKill reads entry's counter now and reports whether the kernel
// killed a process in its session for memory within oomKillWindow, with the
// session's memory limit, 0 for none.
func (s *Scheduler) recentOOMKill(ctx context.Context, entry *supervised) (limit uint64, killed bool) {
	pids, ok := s.cfg.Runtime.(runtime.InitPIDRuntime)
	s.oom.mu.Lock()
	defer s.oom.mu.Unlock()
	if !ok || !s.oom.enable() {
		return 0, false
	}
	s.mu.Lock()
	t := oomTarget{entry.containerID, entry.workspaceID, entry.runID}
	s.mu.Unlock()
	stats := s.checkOOMKill(ctx, pids, t)
	at, known := s.oom.killedAt[t.container]
	return stats.max, known && time.Since(at) < oomKillWindow
}

func (w *oomWatcher) forget(id runtime.ID) {
	delete(w.paths, id)
	delete(w.kills, id)
	delete(w.killedAt, id)
}

func (w *oomWatcher) read(ctx context.Context, pids runtime.InitPIDRuntime, id runtime.ID) (memoryStats, error) {
	if guest, ok := pids.(runtime.MemoryEventsRuntime); ok {
		events, own, err := guest.MemoryEvents(ctx, id)
		if own {
			return memoryStats{oomKills: events.OOMKills, max: events.LimitBytes}, err
		}
	}
	path, ok := w.paths[id]
	if !ok {
		pid, err := pids.InitPID(ctx, id)
		if err != nil {
			return memoryStats{}, err
		}
		if pid <= 0 {
			return memoryStats{}, fmt.Errorf("container %q is not running: %w", id, fs.ErrNotExist)
		}
		if path, err = cgroupPath(w.proc, pid); err != nil {
			return memoryStats{}, err
		}
		w.paths[id] = path
	}
	return readMemoryStats(filepath.Join(w.cgroup, path))
}

// cgroupPath returns the cgroup v2 path of pid from its "0::<path>" line.
func cgroupPath(procRoot string, pid int) (string, error) {
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if path, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return path, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read cgroup of pid %d: %w", pid, err)
	}
	return "", fmt.Errorf("pid %d has no cgroup v2 entry: %w", pid, fs.ErrNotExist)
}

// readMemoryStats reads memory.events, and memory.peak and memory.max when
// the kernel provides them. A max of "max" (unlimited) reads as 0.
func readMemoryStats(dir string) (memoryStats, error) {
	var st memoryStats
	data, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return st, err
	}
	found := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			if st.oomKills, err = strconv.ParseUint(strings.TrimSpace(v), 10, 64); err != nil {
				return st, fmt.Errorf("parse oom_kill in %s: %w", dir, err)
			}
			found = true
		}
	}
	if !found {
		return st, fmt.Errorf("%s/memory.events has no oom_kill field", dir)
	}
	st.peak = readUint(filepath.Join(dir, "memory.peak"))
	st.max = readUint(filepath.Join(dir, "memory.max"))
	return st, nil
}

func readUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func oomMessage(st memoryStats) string {
	msg := "Out of memory: the kernel killed a process in this session"
	if st.max > 0 {
		msg += " at its " + formatGiB(st.max) + " memory limit"
	}
	if st.peak > 0 {
		msg += " (peak " + formatGiB(st.peak) + ")"
	}
	return msg
}

func formatGiB(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}
