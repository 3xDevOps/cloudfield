//go:build kvm

package vm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/guest"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/vmimage"
)

// Scratch measurement (scratch/lazy-adopted-chain); never merged. The
// adopted sleep shape over a long chain: boot with shared memory, a sparse
// first save, the saved configuration rewritten to private memory, every
// wake copy-on-write with a host process reading guest RAM as it starts,
// every later save a reflink clone plus the changed pages.
//
//	LAZY_DIR        data directory on the filesystem under test
//	LAZY_FS         its name, for the log
//	LAZY_DEV_STAT   /sys/class/block/<dev>/stat of the device under it
//	LAZY_MIB        the session's memory limit
//	LAZY_DUMP_DIR   directory on ext4 for full dumps of guest RAM
//	LAZY_MEMCHECK   static zzmemcheck binary
//	LAZY_CYCLES     cycles, 20 unless set
//	LAZY_RUN        seeds the choice of rewritten chunks
//	LAZY_UNCAP      1 lifts the unit's MemoryMax before every save

type zz struct {
	t    *testing.T
	e    *engine
	key  string
	fs   string
	mib  int
	stat string
	dump string

	gens        []uint64
	mainChunks  int
	churnChunks int
	bootID      string
	pid         string
	shmSum      string
	counter     uint64
	chain       [32]byte
	rng         *rand.Rand

	// quiet makes a failed check a caught control, not a test failure.
	quiet     bool
	caught    []string
	staleBase string
	logAt     int
}

func (z *zz) logf(format string, args ...any) {
	z.t.Helper()
	z.t.Logf("ZZ fs=%s mib=%d "+format, append([]any{z.fs, z.mib}, args...)...)
}

func (z *zz) guest(script string) string {
	z.t.Helper()
	out, code := vmRun(z.t, z.e, z.key, script)
	if code != 0 {
		z.t.Fatalf("%q exited %d: %s%s", script, code, out, z.e.logTail(z.key))
	}
	return strings.TrimSpace(out)
}

func (z *zz) call(args string) string {
	z.t.Helper()
	out := z.guest("/usr/local/bin/zzmemcheck call " + args)
	if strings.HasPrefix(out, "error") || out == "" {
		z.t.Fatalf("zzmemcheck %s: %q", strings.Fields(args)[0], out)
	}
	return out
}

func rle(gens []uint64) string {
	var b strings.Builder
	for i := 0; i < len(gens); {
		j := i
		for j < len(gens) && gens[j] == gens[i] {
			j++
		}
		fmt.Fprintf(&b, "%d*%d,", gens[i], j-i)
		i = j
	}
	return b.String()
}

func unrle(s string) []uint64 {
	var out []uint64
	for _, run := range strings.Split(strings.TrimSuffix(s, ","), ",") {
		g, n, _ := strings.Cut(run, "*")
		gen, _ := strconv.ParseUint(g, 10, 64)
		count, _ := strconv.Atoi(n)
		for range count {
			out = append(out, gen)
		}
	}
	return out
}

var verifyLine = regexp.MustCompile(`^bad=(\d+) took=(\S+) first="(.*)" gens=(\S*)$`)

// verify is the full guest check: the boot, the checker's process and its
// live hash chain, the /dev/shm file, and every page of the pattern.
func (z *zz) verify(label string) bool {
	z.t.Helper()
	ok := true
	fail := func(format string, args ...any) {
		ok = false
		if z.quiet {
			z.caught = append(z.caught, fmt.Sprintf(format, args...))
			z.logf("CONTROL-CAUGHT %s: %s", label, fmt.Sprintf(format, args...))
			return
		}
		z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED %s: %s", z.fs, z.mib, label, fmt.Sprintf(format, args...))
	}
	if got := z.guest("cat /proc/sys/kernel/random/boot_id"); got != z.bootID {
		fail("boot_id %q, want %q", got, z.bootID)
	}
	var pid, chain string
	var counter uint64
	if _, err := fmt.Sscanf(z.call("status"), "pid=%s counter=%d chain=%s", &pid, &counter, &chain); err != nil {
		z.t.Fatalf("status: %v", err)
	}
	if pid != z.pid || counter <= z.counter {
		fail("checker pid %s counter %d, want pid %s and a counter past %d", pid, counter, z.pid, z.counter)
	}
	for z.counter < counter {
		z.chain = sha256.Sum256(z.chain[:])
		z.counter++
	}
	if hex.EncodeToString(z.chain[:]) != chain {
		fail("live hash chain at %d is %s, want %s", counter, chain, hex.EncodeToString(z.chain[:]))
	}
	if got := z.guest("sha256sum </dev/shm/fill | cut -d' ' -f1"); got != z.shmSum {
		fail("/dev/shm/fill sha256 %s, want %s", got, z.shmSum)
	}
	m := verifyLine.FindStringSubmatch(z.call("verify"))
	if m == nil {
		z.t.Fatal("verify: unparsed reply")
	}
	want := z.gens
	if m[1] != "0" {
		fail("%s pattern pages are bad; first: %s", m[1], m[3])
	}
	if m[4] != rle(want) {
		got := unrle(m[4])
		shown := 0
		for i := 0; i < len(want) && i < len(got) && shown < 10; i++ {
			if got[i] != want[i] {
				fail("chunk %d has generation %d, want %d", i, got[i], want[i])
				shown++
			}
		}
		if len(got) != len(want) {
			fail("%d chunks, want %d", len(got), len(want))
		}
	}
	z.logf("check %s ok=%v bad_pages=%s chunks=%d guest_verify=%s counter=%d", label, ok, m[1], len(want), m[2], counter)
	return ok
}

func (z *zz) chPID() int {
	z.t.Helper()
	data, err := os.ReadFile(filepath.Join(z.e.cgroupRoot, runtime.SessionSlice, unitName(z.key), "cgroup.procs"))
	if err != nil {
		z.t.Fatal(err)
	}
	for _, f := range strings.Fields(string(data)) {
		if comm, _ := os.ReadFile("/proc/" + f + "/comm"); strings.HasPrefix(string(comm), "cloud-hyperv") {
			pid, _ := strconv.Atoi(f)
			return pid
		}
	}
	z.t.Fatal("no cloud-hypervisor process in the unit")
	return 0
}

type mapping struct{ start, end, offset uint64 }

// cowMappings are Cloud Hypervisor's mappings of the restore source.
func cowMappings(pid int) ([]mapping, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}
	var out []mapping
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.Contains(line, "memory-ranges") {
			continue
		}
		var m mapping
		var perms string
		if _, err := fmt.Sscanf(line, "%x-%x %s %x", &m.start, &m.end, &perms, &m.offset); err != nil {
			return nil, fmt.Errorf("maps line %q: %w", line, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// privateBytes counts, as Cloud Hypervisor's pagemap.rs does, the pages of
// the mapped restore source that no longer read from the file.
func (z *zz) privateBytes(pid int) (private, mapped int64) {
	z.t.Helper()
	maps, err := cowMappings(pid)
	if err != nil {
		z.t.Fatal(err)
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/pagemap", pid))
	if err != nil {
		z.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8<<17)
	for _, m := range maps {
		mapped += int64(m.end - m.start)
		for page := m.start / 4096; page < m.end/4096; {
			n := min(uint64(len(buf)/8), m.end/4096-page)
			if _, err := f.ReadAt(buf[:n*8], int64(page*8)); err != nil {
				z.t.Fatalf("pagemap: %v", err)
			}
			for i := range n {
				if entry := binary.NativeEndian.Uint64(buf[i*8:]); entry&(1<<63|1<<62) != 0 && entry&(1<<61) == 0 {
					private += 4096
				}
			}
			page += n
		}
	}
	return private, mapped
}

// dumpRAM copies guest RAM out of the paused monitor's address space, laid
// out as the memory file is.
func (z *zz) dumpRAM(pid int, path string) int64 {
	z.t.Helper()
	maps, err := cowMappings(pid)
	if err != nil || len(maps) == 0 {
		z.t.Fatalf("no mapped restore source to dump: %v", err)
	}
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		z.t.Fatal(err)
	}
	defer func() { _ = mem.Close() }()
	out, err := os.Create(path)
	if err != nil {
		z.t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	buf := make([]byte, 4<<20)
	var total int64
	for _, m := range maps {
		for at := m.start; at < m.end; {
			n := min(uint64(len(buf)), m.end-at)
			if _, err := io.ReadFull(io.NewSectionReader(mem, int64(at), int64(n)), buf[:n]); err != nil {
				z.t.Fatalf("read guest RAM at %#x: %v", at, err)
			}
			if _, err := out.WriteAt(buf[:n], int64(m.offset+at-m.start)); err != nil {
				z.t.Fatal(err)
			}
			at += n
			total += int64(n)
		}
	}
	return total
}

func diffPages(a, b string) (differing, pages int64, first []int64, err error) {
	fa, err := os.Open(a)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = fa.Close() }()
	fb, err := os.Open(b)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = fb.Close() }()
	sa, _ := fa.Stat()
	sb, _ := fb.Stat()
	if sa.Size() != sb.Size() {
		return 0, 0, nil, fmt.Errorf("sizes differ: %d and %d", sa.Size(), sb.Size())
	}
	ba, bb := make([]byte, 4<<20), make([]byte, 4<<20)
	for {
		na, errA := io.ReadFull(fa, ba)
		nb, errB := io.ReadFull(fb, bb)
		if na != nb {
			return 0, 0, nil, fmt.Errorf("short read: %d and %d (%v, %v)", na, nb, errA, errB)
		}
		for p := 0; p < na; p += 4096 {
			end := min(p+4096, na)
			if !bytes.Equal(ba[p:end], bb[p:end]) {
				differing++
				if len(first) < 8 {
					first = append(first, pages)
				}
			}
			pages++
		}
		if errA != nil {
			return differing, pages, first, nil
		}
	}
}

// extents reads filefrag -v: the extents of path, how many are shared with
// another file, and the bytes in those that are not.
func extents(path string) (count, shared int, unsharedBytes int64, summary string) {
	out, err := exec.Command("filefrag", "-v", path).CombinedOutput()
	if err != nil {
		return 0, 0, 0, fmt.Sprintf("filefrag: %v: %s", err, out)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 5 {
			if strings.Contains(line, "extent") && strings.Contains(line, "found") {
				summary = strings.TrimSpace(line)
			}
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(f[0])); err != nil {
			continue
		}
		blocks, err := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		if err != nil {
			continue
		}
		count++
		if strings.Contains(f[len(f)-1], "shared") {
			shared++
		} else {
			unsharedBytes += blocks * 4096
		}
	}
	return count, shared, unsharedBytes, summary
}

func (z *zz) devWritten() int64 {
	data, err := os.ReadFile(z.stat)
	if err != nil {
		z.t.Fatal(err)
	}
	f := strings.Fields(string(data))
	sectors, _ := strconv.ParseInt(f[6], 10, 64)
	return sectors * 512
}

func (z *zz) cgroupWritten() (int64, string) {
	data, err := os.ReadFile(filepath.Join(z.e.cgroupRoot, runtime.SessionSlice, unitName(z.key), "io.stat"))
	if err != nil {
		return -1, err.Error()
	}
	var total int64
	for _, f := range strings.Fields(string(data)) {
		if v, ok := strings.CutPrefix(f, "wbytes="); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			total += n
		}
	}
	return total, strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", " | ")
}

func (z *zz) syncfs() {
	d, err := os.Open(z.e.root)
	if err == nil {
		err = errors.Join(unix.Syncfs(int(d.Fd())), d.Close())
	}
	if err != nil {
		z.t.Fatal(err)
	}
}

func (z *zz) fsUsed() int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(z.e.root, &st); err != nil {
		z.t.Fatal(err)
	}
	return int64(st.Blocks-st.Bfree) * st.Bsize
}

// kernelLog prints the host kernel's last out-of-memory report.
func (z *zz) kernelLog(label string) {
	out, _ := exec.Command("dmesg").Output()
	lines := strings.Split(string(out), "\n")
	from := -1
	for i, line := range lines {
		if strings.Contains(line, "invoked oom-killer") {
			from = i
		}
	}
	if from < 0 {
		z.logf("dmesg %s: no out-of-memory report", label)
		return
	}
	for _, line := range lines[from:min(from+90, len(lines))] {
		z.logf("dmesg %s: %s", label, line)
	}
}

// memcg is the unit's memory as its cgroup counts it.
func (z *zz) memcg() string {
	dir := filepath.Join(z.e.cgroupRoot, runtime.SessionSlice, unitName(z.key))
	current, _ := os.ReadFile(filepath.Join(dir, "memory.current"))
	limit, _ := os.ReadFile(filepath.Join(dir, "memory.max"))
	events, _ := os.ReadFile(filepath.Join(dir, "memory.events"))
	stat, _ := os.ReadFile(filepath.Join(dir, "memory.stat"))
	var kept []string
	for line := range strings.SplitSeq(string(stat), "\n") {
		switch name, _, _ := strings.Cut(line, " "); name {
		case "anon", "file", "file_mapped", "file_dirty", "file_writeback":
			kept = append(kept, line)
		}
	}
	return fmt.Sprintf("current=%s max=%s %s events=%s", strings.TrimSpace(string(current)), strings.TrimSpace(string(limit)),
		strings.Join(kept, " "), strings.Join(strings.Fields(string(events)), " "))
}

// otherBytes is what everything in the data directory but the saved states
// holds on disk: the image store and the VM's disks.
func (z *zz) otherBytes() int64 {
	seen := map[uint64]bool{}
	var total int64
	_ = filepath.WalkDir(z.e.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), "snapshot") {
			return filepath.SkipDir
		}
		var st unix.Stat_t
		if unix.Lstat(path, &st) == nil && !seen[st.Ino] {
			seen[st.Ino] = true
			total += st.Blocks * 512
		}
		return nil
	})
	return total
}

func (z *zz) privateMiB() float64 {
	p, _ := z.privateBytes(z.chPID())
	return float64(p) / (1 << 20)
}

// hypervisorLog prints what the monitor logged since the last call, and
// fails the test when it gave up the copy-on-write restore or the reflink.
func (z *zz) hypervisorLog(label string) {
	data, _ := os.ReadFile(z.e.path(z.key, monitorLog))
	if len(data) < z.logAt {
		z.logAt = 0
	}
	fresh := string(data[z.logAt:])
	z.logAt = len(data)
	n := 0
	for line := range strings.SplitSeq(fresh, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "copyonwrite") || strings.Contains(l, "snapshot") || strings.Contains(l, "warn") || strings.Contains(l, "error") || strings.Contains(l, "falling back") {
			z.logf("chlog %s: %s", label, line)
			n++
		}
		if strings.Contains(l, "falling back") || strings.Contains(l, "not cloned") || strings.Contains(l, "all guest memory") {
			z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED %s: the monitor left the adopted path: %s", z.fs, z.mib, label, line)
		}
	}
	z.logf("chlog %s: %d matching lines of %d new bytes of monitor log", label, n, len(fresh))
}

type saveResult struct {
	snapshot, fsync time.Duration
	devBytes        int64
}

// save pauses the VM and snapshots it into into, measuring what that wrote.
// The VM stays paused and its monitor up.
func (z *zz) snapshot(label, into string, dump bool, changedMiB int) (saveResult, error) {
	z.t.Helper()
	ctx := z.t.Context()
	api := newCHAPI(z.e.path(z.key, apiSocket))
	if state, _ := api.state(ctx); state != "Paused" {
		if err := api.pause(ctx); err != nil {
			z.t.Fatalf("pause: %v", err)
		}
	}
	pid := z.chPID()
	private, mapped := z.privateBytes(pid)
	dumpPath := filepath.Join(z.dump, fmt.Sprintf("dump-%s-%d", z.fs, z.mib))
	if dump {
		start := time.Now()
		n := z.dumpRAM(pid, dumpPath)
		z.logf("dump %s bytes=%d took=%s", label, n, time.Since(start).Round(time.Millisecond))
		defer func() { _ = os.Remove(dumpPath) }()
	}
	if os.Getenv("LAZY_UNCAP") == "1" {
		if out, err := exec.Command("systemctl", "set-property", "--runtime", unitName(z.key), "MemoryMax=infinity").CombinedOutput(); err != nil {
			z.t.Fatalf("uncap the unit: %v: %s", err, out)
		}
	}
	memBefore := z.memcg()
	z.syncfs()
	dev0 := z.devWritten()
	cg0, _ := z.cgroupWritten()
	start := time.Now()
	snapCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	err := api.snapshot(snapCtx, into)
	cancel()
	res := saveResult{snapshot: time.Since(start)}
	if err != nil {
		return res, err
	}
	start = time.Now()
	if err = syncSnapshot(into); err != nil {
		z.t.Fatal(err)
	}
	res.fsync = time.Since(start)
	z.syncfs()
	res.devBytes = z.devWritten() - dev0
	cg1, cgRaw := z.cgroupWritten()
	file := filepath.Join(into, "memory-ranges")
	var st unix.Stat_t
	if err = unix.Stat(file, &st); err != nil {
		z.t.Fatal(err)
	}
	count, shared, unshared, summary := extents(file)
	z.logf("save %s snapshot=%dms fsync=%dms dev_written_mib=%.1f cgroup_wbytes_mib=%.1f changed_mib=%d private_mib=%.1f cow_mapped_mib=%d "+
		"file_size_mib=%d file_allocated_mib=%d extents=%d shared_extents=%d unshared_mib=%.1f filefrag=%q",
		label, res.snapshot.Milliseconds(), res.fsync.Milliseconds(), float64(res.devBytes)/(1<<20), float64(cg1-cg0)/(1<<20), changedMiB,
		float64(private)/(1<<20), mapped>>20, st.Size>>20, st.Blocks*512>>20, count, shared, float64(unshared)/(1<<20), summary)
	z.logf("iostat %s: %s", label, cgRaw)
	z.logf("memcg %s before=[%s] after=[%s]", label, memBefore, z.memcg())
	z.hypervisorLog("after save " + label)
	if dump {
		start = time.Now()
		differing, pages, first, diffErr := diffPages(dumpPath, file)
		if diffErr != nil {
			z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED page compare %s: %v", z.fs, z.mib, label, diffErr)
		} else if differing != 0 {
			z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED page compare %s: %d of %d pages differ, first %v", z.fs, z.mib, label, differing, pages, first)
		}
		z.logf("pagecompare %s differing_pages=%d pages=%d err=%v took=%s", label, differing, pages, diffErr, time.Since(start).Round(time.Millisecond))
		if z.staleBase != "" {
			differing, pages, _, diffErr = diffPages(dumpPath, z.staleBase)
			if diffErr != nil || differing == 0 {
				z.t.Errorf("ZZ fs=%s mib=%d CONTROL-MISSED page compare %s against the stale base: %d pages differ, %v", z.fs, z.mib, label, differing, diffErr)
			}
			z.logf("pagecompare-control %s against_stale_base differing_pages=%d pages=%d err=%v", label, differing, pages, diffErr)
		}
	}
	return res, nil
}

// sleep records the VM as saved and ends its monitor, as engine.save does.
func (z *zz) sleep() {
	z.t.Helper()
	ctx := z.t.Context()
	unlock := z.e.lock(z.key)
	defer unlock()
	rec, err := z.e.load(z.key)
	if err != nil {
		z.t.Fatal(err)
	}
	rec.Saved, rec.Resumed, rec.Exited, rec.ExitCode = true, nil, true, killedExitCode
	rec.CacheSynced = false
	if err = z.e.store(rec); err != nil {
		z.t.Fatal(err)
	}
	z.e.forgetHelper(z.key)
	if err = newCHAPI(z.e.path(z.key, apiSocket)).shutdownVMM(ctx); err != nil {
		z.t.Fatalf("shut the monitor down: %v", err)
	}
	if err = z.e.awaitStopped(ctx, z.key); err != nil {
		z.t.Fatal(err)
	}
}

func (z *zz) settle(label string) {
	before := z.privateMiB()
	start := time.Now()
	if !z.e.syncGuest(z.t.Context(), z.key) {
		z.t.Fatal("guest sync failed")
	}
	synced := z.privateMiB()
	z.e.awaitReported(z.t.Context(), z.key)
	reported := z.privateMiB()
	z.e.syncGuest(z.t.Context(), z.key)
	z.logf("settle %s took=%dms private_mib before=%.1f after_sync=%.1f after_reporting=%.1f after_second_sync=%.1f", label, time.Since(start).Milliseconds(), before, synced, reported, z.privateMiB())
}

func (z *zz) wake(label string, cold bool) {
	z.t.Helper()
	if cold {
		unix.Sync()
		if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0); err != nil {
			z.t.Fatal(err)
		}
	}
	start := time.Now()
	inj := zzInject(filepath.Join(z.e.cgroupRoot, runtime.SessionSlice, unitName(z.key)), start)
	err := z.e.restore(z.t.Context(), z.key)
	took := time.Since(start)
	inj.end()
	for _, line := range inj.lines {
		z.logf("inj %s %s", label, line)
	}
	if err != nil {
		z.t.Fatalf("wake %s: %v%s", label, err, z.e.logTail(z.key))
	}
	private, mapped := z.privateBytes(z.chPID())
	z.logf("wake %s cold_cache=%v took=%dms cow_mapped_mib=%d private_mib=%.1f inj_rounds=%d inj_reads=%d inj_zero=%d inj_err=%d inj_first_maps_ms=%.2f",
		label, cold, took.Milliseconds(), mapped>>20, float64(private)/(1<<20), inj.rounds, inj.reads, inj.zero, inj.failed, inj.firstMapsMS)
	if inj.reads-inj.failed == 0 {
		z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED wake %s: the early reader read no guest RAM", z.fs, z.mib, label)
	}
	if mapped>>20 != guestMiB(int64(z.mib)) {
		z.t.Errorf("ZZ fs=%s mib=%d CHECK-FAILED wake %s: %d MiB of guest RAM is mapped from the saved file, want %d", z.fs, z.mib, label, mapped>>20, guestMiB(int64(z.mib)))
	}
	z.hypervisorLog("after wake " + label)
}

func (z *zz) rewrite(gen uint64, chunks int) []int {
	picked := z.rng.Perm(z.mainChunks)[:chunks]
	args := make([]string, len(picked))
	for i, c := range picked {
		args[i] = strconv.Itoa(c)
		z.gens[c] = gen
	}
	z.call(fmt.Sprintf("rewrite %d %s", gen, strings.Join(args, " ")))
	return picked
}

// churn frees the churn region and fills it again, twice: the first refill
// makes its pages private copies, so the second free has the host drop
// private copies that the last refill then takes again.
func (z *zz) churn(label string, gen uint64) {
	pid := z.chPID()
	private := func() float64 {
		p, _ := z.privateBytes(pid)
		return float64(p) / (1 << 20)
	}
	before := private()
	z.call("free")
	time.Sleep(10 * time.Second)
	freed := private()
	z.call(fmt.Sprintf("realloc %d", gen))
	refilled := private()
	z.call("free")
	low := refilled
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		if low = private(); low < refilled-float64(z.churnChunks)*0.8 {
			break
		}
	}
	z.call(fmt.Sprintf("realloc %d", gen+1000))
	for i := z.mainChunks; i < len(z.gens); i++ {
		z.gens[i] = gen + 1000
	}
	z.logf("churn %s churn_mib=%d private_mib before=%.1f after_free=%.1f after_refill=%.1f after_second_free=%.1f after_second_refill=%.1f",
		label, z.churnChunks, before, freed, refilled, low, private())
}

func TestKVMZZAdoptedChain(t *testing.T) {
	dir, kernel, server := os.Getenv("LAZY_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	if dir == "" {
		t.Skip("set LAZY_DIR")
	}
	mib, err := strconv.Atoi(os.Getenv("LAZY_MIB"))
	if err != nil {
		t.Fatal(err)
	}
	cycles := 20
	if v := os.Getenv("LAZY_CYCLES"); v != "" {
		if cycles, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}
	run, _ := strconv.Atoi(os.Getenv("LAZY_RUN"))
	zzShared, zzRestoreOpts, zzRestoreArgs, zzKeepSource = "on", "", []string{"-v"}, true
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(Config{DataDir: dir, Images: images, Monitor: []string{server, MonitorCommand}, Helper: server, ProgramUnit: localBinProgram}, SystemdUnits{}, dialHelper)
	z := &zz{t: t, e: e, key: "run-zz-incr", fs: os.Getenv("LAZY_FS"), mib: mib, stat: os.Getenv("LAZY_DEV_STAT"), dump: os.Getenv("LAZY_DUMP_DIR"),
		chain: sha256.Sum256([]byte("zzmemcheck")), rng: rand.New(rand.NewSource(int64(mib)*100 + int64(run)))}
	t.Cleanup(func() { _ = e.destroy(context.Background(), z.key) })
	shmMiB := 128
	prepare := func() {
		z.counter, z.chain = 0, sha256.Sum256([]byte("zzmemcheck"))
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: z.key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": z.key}, CPULimit: 2, MemoryLimitBytes: int64(mib) << 20,
		}
		if _, err = e.create(t.Context(), z.key, spec, options{}); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err = e.start(t.Context(), z.key); err != nil {
			t.Fatal(err)
		}
		z.logf("boot took=%dms", time.Since(start).Milliseconds())
		props, _ := exec.Command("systemctl", "show", "--property=DevicePolicy,DeviceAllow,ProtectKernelTunables,NoNewPrivileges,ProtectSystem,PrivateMounts,IOAccounting,MemoryMax,CapabilityBoundingSet,BindPaths,TemporaryFileSystem", unitName(z.key)).Output()
		z.logf("unit %s", strings.ReplaceAll(strings.TrimSpace(string(props)), "\n", " ; "))
		version, _ := exec.Command("cloud-hypervisor", "--version").Output()
		launched, _ := os.ReadFile(e.path(z.key, launchFile))
		z.logf("hypervisor %s memory_arg=%q", strings.TrimSpace(string(version)), regexp.MustCompile(`size=\d+M,shared=\w+`).Find(launched))

		checker, err := os.ReadFile(os.Getenv("LAZY_MEMCHECK"))
		if err != nil {
			t.Fatal(err)
		}
		p, att, err := e.spawn(t.Context(), z.key, guest.ProcessSpec{ID: processID("exec"), Argv: []string{"/bin/sh", "-c", "cat >/usr/local/bin/zzmemcheck && chmod 755 /usr/local/bin/zzmemcheck"}, Lossless: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = att.Stdin().Write(checker); err != nil {
			t.Fatal(err)
		}
		if err = p.CloseStdin(t.Context()); err != nil {
			t.Fatal(err)
		}
		if code, waitErr := p.Wait(t.Context()); code != 0 || waitErr != nil {
			t.Fatalf("copy the checker into the guest: %d, %v", code, waitErr)
		}
		_ = att.Close()
		sum := sha256.Sum256(checker)
		if got := z.guest("sha256sum </usr/local/bin/zzmemcheck | cut -d' ' -f1"); got != hex.EncodeToString(sum[:]) {
			t.Fatalf("the checker arrived as %s", got)
		}
		z.guest("setsid /usr/local/bin/zzmemcheck serve </dev/null >/var/log/zzmemcheck.log 2>&1 & sleep 1; test -S /run/zzmemcheck.sock")
		z.churnChunks = 512
		z.mainChunks = mib*5/8 - z.churnChunks
		if mib >= 8192 {
			shmMiB = 256
		}
		z.gens = make([]uint64, z.mainChunks+z.churnChunks)
		for i := range z.gens {
			z.gens[i] = 1
		}
		z.bootID = z.guest("cat /proc/sys/kernel/random/boot_id")
		z.shmSum = z.guest(fmt.Sprintf("mount -o remount,size=90%% /dev/shm; head -c %dM /dev/urandom >/dev/shm/fill; sha256sum </dev/shm/fill | cut -d' ' -f1", shmMiB))
		z.logf("fill %s pattern_mib=%d shm_mib=%d guest_meminfo=%q", z.call(fmt.Sprintf("fill %d %d 1", z.mainChunks, z.churnChunks)), len(z.gens), shmMiB,
			z.guest("grep -E '^(MemTotal|MemFree|MemAvailable|Mlocked|SwapTotal|SwapFree):' /proc/meminfo | tr -s ' \\n' ' '"))
		if _, err = fmt.Sscanf(z.call("status"), "pid=%s", &z.pid); err != nil {
			t.Fatal(err)
		}
		z.verify("after fill")
	}
	prepare()

	snapDir, nextDir := e.path(z.key, snapshotDir), e.path(z.key, "snapshot.next")
	firstSave := func(label string) error {
		z.settle(label)
		if err = os.Mkdir(snapDir, 0o700); err != nil {
			t.Fatal(err)
		}
		_, saveErr := z.snapshot(label, snapDir, false, len(z.gens)+shmMiB)
		return saveErr
	}
	if err = firstSave("0 first"); err != nil {
		z.logf("FIRST-SAVE-FAILED at the unit's own MemoryMax: %v", err)
		z.kernelLog("first save")
		if err = e.destroy(t.Context(), z.key); err != nil {
			t.Fatal(err)
		}
		prepare()
		out, setErr := exec.Command("systemctl", "set-property", "--runtime", unitName(z.key), "MemoryMax=infinity").CombinedOutput()
		z.logf("first save again with MemoryMax=infinity on the unit: %s %v", out, setErr)
		if err = firstSave("0 first uncapped"); err != nil {
			z.kernelLog("first save uncapped")
			t.Fatalf("first save with MemoryMax=infinity: %v%s", err, e.logTail(z.key))
		}
	}
	z.sleep()
	z.logf("rest 0 fs_used_mib=%d other_files_mib=%d", z.fsUsed()>>20, z.otherBytes()>>20)
	was, now, err := zzPrivateConfig(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	z.logf("config rewritten memory_was=%q memory_now=%q", was, now)
	zzRestoreOpts = ",memory_restore_mode=copyonwrite"

	// mode full: check, rewrite 64 MiB, and churn every third cycle;
	// checked: the full check and no other work; untouched: one command.
	cycle := func(n int, mode string) {
		label := strconv.Itoa(n)
		z.wake(label, n%7 == 0)
		woke := z.privateMiB()
		changed := 0
		if mode == "untouched" {
			z.guest("true")
		} else if !z.verify("after wake " + label) {
			t.Fatalf("cycle %d: the guest check failed%s", n, e.logTail(z.key))
		}
		checked := z.privateMiB()
		if mode == "full" {
			changed = 64
			z.rewrite(uint64(n+1), 64)
		}
		rewritten := z.privateMiB()
		if mode == "full" && n%3 == 0 {
			z.churn(label, uint64(n+1))
			changed += z.churnChunks
			label += " churn"
		}
		if mode != "full" {
			label += " " + mode
		}
		z.logf("private %s private_mib after_wake=%.1f after_check=%.1f after_rewrite=%.1f", label, woke, checked, rewritten)
		z.settle(label)
		if err = os.Mkdir(nextDir, 0o700); err != nil {
			t.Fatal(err)
		}
		used := z.fsUsed()
		if _, err = z.snapshot(label, nextDir, n == 1 || n == cycles/2 || n == cycles, changed); err != nil {
			z.kernelLog("save " + label)
			t.Fatalf("save %d: %v%s", n, err, e.logTail(z.key))
		}
		both := z.fsUsed()
		z.sleep()
		if err = errors.Join(os.RemoveAll(snapDir), os.Rename(nextDir, snapDir)); err != nil {
			t.Fatal(err)
		}
		z.syncfs()
		count, shared, _, _ := extents(filepath.Join(snapDir, "memory-ranges"))
		z.logf("rest %s fs_used_mib before_save=%d with_both=%d after_swap=%d other_files_mib=%d extents=%d shared_extents=%d",
			label, used>>20, both>>20, z.fsUsed()>>20, z.otherBytes()>>20, count, shared)
	}
	for n := 1; n <= cycles; n++ {
		cycle(n, "full")
	}
	cycle(cycles+1, "checked")
	cycle(cycles+2, "untouched")
	cycle(cycles+3, "untouched")

	z.staleControl(snapDir, nextDir, uint64(cycles+5))

	z.wake("final", false)
	if !z.verify("after wake final") {
		t.Fatalf("the guest check failed after the last save%s", e.logTail(z.key))
	}
}
