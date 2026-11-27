//go:build kvm

package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

const (
	sleepWakeCycles = 12
	memcheckCall    = "/usr/local/bin/memcheck -test.run '^TestKVMMemcheckNow$'"
	// sleepWakeScript prints what a wake must leave as it was: the
	// session's processes, the file in /dev/shm, a kernel log free of
	// trouble, and the checker's answer.
	sleepWakeScript = "set -e; log=$(dmesg); echo MAIN $(pgrep -x sleep | sort); echo SHM $(sha256sum </dev/shm/fill); " +
		"echo TROUBLE $(echo \"$log\" | grep -cE 'Kernel panic|BUG|Oops|Bad page|bad pte|general protection|Call Trace|rcu.*stall|soft lockup|segfault|Out of memory|oom-kill'); " +
		"AETHER_VM_TEST_MEMCHECK=verify " + memcheckCall
)

var errGeneration = errors.New("a chunk of the pattern holds another generation")

// TestKVMSleepWakeChain saves and wakes one 2 GiB session VM twelve times
// and checks after every wake that the guest is the one that went to
// sleep: the same boot and processes, a 1024 MiB pattern in which every
// page names its index and the generation that last wrote it, the sha256
// of a file in /dev/shm, a loop that takes and frees memory still turning,
// and a kernel log with no trouble in it. The host keeps the generation
// each MiB of the pattern must hold and has a third rewritten after every
// wake, so a page from an older save cannot pass. On the first, middle and
// last cycle the saved state is also loaded as a wake loads it and, with
// the guest still paused, saved again: the two memory files must be equal.
//
// Two controls prove the checks can fail. A wake from the save before the
// last must fail the generation check. A wake from a memory file cut to
// half its length must fail, or leave a guest that fails the checks:
// Cloud Hypervisor v54.1 loads such a file, and the guest then crashes.
//
// The pattern is half the session's limit, not more: with 1280 MiB the
// monitor of one run in three was killed mid-save, its unit out of memory
// from the guest's memory plus the page cache of the file being written.
func TestKVMSleepWakeChain(t *testing.T) { sleepWakeChain(t, "run-sleepwake", 2048, 1024) }

// TestLargeKVMSleepWakeChain is the chain at 8 GiB, where Cloud Hypervisor
// maps the guest's memory past 4 GiB apart from the first 3 GiB. Its name
// keeps it out of the tests make test-kvm runs unless KVM_RUN names it: it
// takes too long for every run.
func TestLargeKVMSleepWakeChain(t *testing.T) { sleepWakeChain(t, "run-sleepwake-large", 8192, 5120) }

type sleepWake struct {
	t   *testing.T
	e   *engine
	key string
	// gens is the generation each MiB chunk of the pattern was last
	// written with.
	gens       []uint64
	shm, alive string
	rounds     uint64
}

func sleepWakeChain(t *testing.T, key string, limitMiB, patternMiB int) {
	e := kvmEngine(t)
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 2, MemoryLimitBytes: int64(limitMiB) << 20,
		Mounts: []runtime.Mount{{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true}},
	}
	if _, err = e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err = e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	s := &sleepWake{t: t, e: e, key: key, gens: make([]uint64, patternMiB)}
	for c := range s.gens {
		s.gens[c] = 1
	}
	// The checker runs from the VM's own disk, so no page of it comes
	// through the file service.
	if _, err = s.guest(fmt.Sprintf("set -e; cp /opt/aether-vmtest /usr/local/bin/memcheck; head -c %dM /dev/urandom >/dev/shm/fill; "+
		"AETHER_VM_TEST_MEMCHECK=%d setsid %s -test.timeout 0 </dev/null >/var/tmp/memcheck.log 2>&1 &", limitMiB/16, len(s.gens), memcheckCall)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		if err = s.check(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			log, _ := s.guest("cat /var/tmp/memcheck.log")
			t.Fatalf("the checker did not start: %v: %v", err, log)
		}
	}

	dir, older := e.path(key, snapshotDir), e.path(key, "snapshot.older")
	// before is what the guest held at the last save of the chain.
	var before sleepWake
	for cycle := 1; cycle <= sleepWakeCycles; cycle++ {
		start := time.Now()
		s.save()
		saved, _ := e.savedMemory(key)
		saveTook := time.Since(start)
		if cycle == 1 || cycle == sleepWakeCycles/2 || cycle == sleepWakeCycles {
			s.comparePages()
		}
		if cycle == sleepWakeCycles {
			s.keep(dir, older)
			before = sleepWake{gens: slices.Clone(s.gens), rounds: s.rounds}
		}
		// Every other wake reads its memory from the disk, as after a long
		// sleep on a busy host; the rest find it in the page cache.
		if cycle%2 == 1 {
			unix.Sync()
			if err = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0); err != nil {
				t.Fatal(err)
			}
		}
		start = time.Now()
		if err = e.restore(t.Context(), key); err != nil {
			t.Fatalf("wake %d: %v", cycle, err)
		}
		t.Logf("cycle %d: the save took %s and wrote %d MiB, the wake %s", cycle, saveTook.Round(time.Millisecond), saved>>20, time.Since(start).Round(time.Millisecond))
		if err = s.check(); err != nil {
			t.Fatalf("after wake %d: %v%s", cycle, err, e.logTail(key))
		}
		s.rewrite(uint64(cycle) + 1)
	}

	// The wake from the save before the last: the guest it leaves holds
	// what the pattern was before the last rewrite.
	s.save()
	if err = os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(older, dir); err != nil {
		t.Fatal(err)
	}
	if err = e.restore(t.Context(), key); err != nil {
		t.Fatalf("wake from the save before the last: %v", err)
	}
	if err = s.check(); !errors.Is(err, errGeneration) {
		t.Fatalf("after a wake from the save before the last the check said %v, want %v", err, errGeneration)
	}
	t.Logf("after the wake from the save before the last: %v", err)

	// The wake from a memory file cut to half, saved from that guest and
	// checked against what it held.
	s.gens, s.rounds = before.gens, before.rounds
	s.save()
	memory := filepath.Join(dir, "memory-ranges")
	info, err := os.Stat(memory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(memory, info.Size()/2); err != nil {
		t.Fatal(err)
	}
	if err = e.restore(t.Context(), key); err == nil {
		err = s.check()
	}
	if err == nil {
		t.Fatalf("a wake from a memory file cut from %d bytes to half left a guest that passes the checks", info.Size())
	}
	t.Logf("the wake from a memory file cut to half: %v", err)
}

// guest runs script in the session and returns its output lines by their
// first word.
func (s *sleepWake) guest(script string) (map[string]string, error) {
	code, out, errOut, err := s.e.exec(s.t.Context(), s.key, []string{"/bin/sh", "-c", script}, "")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("%q exited %d, %v: %s%s", script, code, err, out, errOut)
	}
	lines := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		name, rest, _ := strings.Cut(line, " ")
		lines[name] = rest
	}
	return lines, nil
}

func (s *sleepWake) save() {
	s.t.Helper()
	if err := s.e.save(s.t.Context(), s.key); err != nil {
		s.t.Fatal(err)
	}
}

// check compares the guest with what the last check and the rewrites since
// left. The first one records it.
func (s *sleepWake) check() error {
	lines, err := s.guest(sleepWakeScript)
	if err != nil {
		return err
	}
	var id, gens string
	var rounds, torn uint64
	var bad int
	if _, err = fmt.Sscanf(lines["MEMCHECK"], "id=%s rounds=%d torn=%d bad=%d gens=%s", &id, &rounds, &torn, &bad, &gens); err != nil {
		return fmt.Errorf("the checker answered %q: %w", lines["MEMCHECK"], err)
	}
	alive := id + " " + lines["MAIN"]
	if s.alive == "" {
		s.alive, s.shm = alive, lines["SHM"]
	}
	if alive != s.alive {
		return fmt.Errorf("the guest's checker, boot and processes are %q, want %q", alive, s.alive)
	}
	if bad != 0 {
		return fmt.Errorf("%d pages of the pattern are not whole pages of their chunk's generation", bad)
	}
	chunk := 0
	for run := range strings.SplitSeq(gens, ",") {
		var gen uint64
		var n int
		if _, err = fmt.Sscanf(run, "%d*%d", &gen, &n); err != nil || chunk+n > len(s.gens) {
			return fmt.Errorf("the checker's generations %q: %v", gens, err)
		}
		for ; n > 0; n, chunk = n-1, chunk+1 {
			if gen != s.gens[chunk] {
				return fmt.Errorf("%w: chunk %d holds generation %d, want %d", errGeneration, chunk, gen, s.gens[chunk])
			}
		}
	}
	if chunk != len(s.gens) {
		return fmt.Errorf("the checker read %d chunks, want %d", chunk, len(s.gens))
	}
	if lines["SHM"] != s.shm {
		return fmt.Errorf("the file in /dev/shm has sha256 %q, want %q", lines["SHM"], s.shm)
	}
	if lines["TROUBLE"] != "0" {
		return fmt.Errorf("the guest's kernel log has %s lines of trouble", lines["TROUBLE"])
	}
	for deadline := time.Now().Add(30 * time.Second); rounds <= s.rounds; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("the checker's loop stays at round %d", rounds)
		}
		if lines, err = s.guest("AETHER_VM_TEST_MEMCHECK=rounds " + memcheckCall); err != nil {
			return err
		}
		if _, err = fmt.Sscanf(lines["MEMCHECK"], "id=%s rounds=%d torn=%d", &id, &rounds, &torn); err != nil {
			return fmt.Errorf("the checker answered %q: %w", lines["MEMCHECK"], err)
		}
	}
	if torn != 0 {
		return fmt.Errorf("the checker's loop read back %d rounds changed", torn)
	}
	s.rounds = rounds
	return nil
}

// rewrite has every third chunk of the pattern stamped with gen.
func (s *sleepWake) rewrite(gen uint64) {
	s.t.Helper()
	rem := int(gen % 3)
	if _, err := s.guest(fmt.Sprintf("AETHER_VM_TEST_MEMCHECK='rewrite %d %d' %s", gen, rem, memcheckCall)); err != nil {
		s.t.Fatal(err)
	}
	for c := rem; c < len(s.gens); c += 3 {
		s.gens[c] = gen
	}
}

// keep links the saved state's files into to, where they outlive the wake
// that deletes the saved state.
func (s *sleepWake) keep(dir, to string) {
	s.t.Helper()
	entries, err := os.ReadDir(dir)
	if err == nil {
		err = os.Mkdir(to, 0o700)
	}
	for _, entry := range entries {
		if err == nil {
			err = os.Link(filepath.Join(dir, entry.Name()), filepath.Join(to, entry.Name()))
		}
	}
	if err != nil {
		s.t.Fatal(err)
	}
}

// comparePages loads the saved state as a wake does, saves the still
// paused guest again and fails on any page the two memory files differ in.
// One place may differ: a restore sets the MSR that names the guest's wall
// clock, and KVM then writes the clock there.
func (s *sleepWake) comparePages() {
	t, e := s.t, s.e
	t.Helper()
	dir, again := e.path(s.key, snapshotDir), e.path(s.key, "snapshot.again")
	clock, err := wallClock(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.load(s.key)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.loadSaved(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(again, 0o700); err == nil {
		err = newCHAPI(e.path(s.key, apiSocket)).snapshot(t.Context(), again)
	}
	e.kill(context.Background(), s.key)
	if err != nil {
		t.Fatal(err)
	}
	differ, err := differingPages(filepath.Join(dir, "memory-ranges"), filepath.Join(again, "memory-ranges"), clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(differ) > 0 {
		t.Fatalf("%d pages of the loaded memory differ from the saved file, the first at %#x; the wall clock is at %#x", len(differ), differ[:min(len(differ), 16)], clock)
	}
	if err = os.RemoveAll(again); err != nil {
		t.Fatal(err)
	}
}

// wallClock is the guest physical address of the 12-byte struct
// pvclock_wall_clock, the value of MSR_KVM_WALL_CLOCK_NEW in the saved
// vCPU state in dir.
func wallClock(dir string) (int64, error) {
	state, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return 0, err
	}
	// The vCPU state is JSON inside a JSON string, so its quotes are
	// escaped.
	values := map[int64]bool{}
	for _, m := range regexp.MustCompile(`\\*"index\\*":\s*1263947008,\s*\\*"data\\*":\s*(\d+)`).FindAllSubmatch(state, -1) {
		v, parseErr := strconv.ParseInt(string(m[1]), 10, 64)
		if parseErr != nil {
			return 0, parseErr
		}
		values[v] = true
	}
	// Below 3 GiB an address is its offset in the memory file.
	for v := range values {
		if len(values) == 1 && v > 0 && v < 3<<30 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("the saved vCPUs have MSR_KVM_WALL_CLOCK_NEW values %v, want one address below 3 GiB", values)
}

// differingPages is the offsets of the pages two memory files differ in,
// a hole read as zeros. The 12 bytes at clock may differ.
func differingPages(a, b string, clock int64) ([]int64, error) {
	var files [2]*os.File
	var sizes [2]int64
	for i, path := range []string{a, b} {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		files[i], sizes[i] = f, info.Size()
	}
	if sizes[0] != sizes[1] {
		return nil, fmt.Errorf("the memory files are %d and %d bytes long", sizes[0], sizes[1])
	}
	const page, chunk = 4096, 4 << 20
	var differ []int64
	bufs := [2][]byte{make([]byte, chunk), make([]byte, chunk)}
	for off := int64(0); off < sizes[0]; off += chunk {
		n := min(chunk, sizes[0]-off)
		for i, f := range files {
			if _, err := f.ReadAt(bufs[i][:n], off); err != nil {
				return nil, err
			}
		}
		if at := clock - off; at >= 0 && at+12 <= n {
			copy(bufs[1][at:at+12], bufs[0][at:at+12])
		}
		if bytes.Equal(bufs[0][:n], bufs[1][:n]) {
			continue
		}
		for p := int64(0); p < n; p += page {
			if !bytes.Equal(bufs[0][p:min(p+page, n)], bufs[1][p:min(p+page, n)]) {
				differ = append(differ, off+p)
			}
		}
	}
	return differ, nil
}
