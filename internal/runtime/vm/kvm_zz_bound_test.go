//go:build kvm

package vm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Scratch measurement (scratch/lazy-file-bound); never merged. In the adopted
// shape (shared boot, sparse first save, configuration rewritten to private
// memory, copy-on-write wakes, changed-pages saves), what keeps the saved
// memory file close to the guest's live memory over a chain of sleeps?
//
//	LAZY_ARM   A today's guest, B A + holes dug after every save,
//	           C init_on_free=1 + holes dug, D init_on_free=1 alone,
//	           E C + zero pages punched out of the booted VM,
//	           G today's guest; before every save the frames the guest
//	           lists as free are punched out of the mapped memory file,
//	           cost: the build timing below instead of the chain

// lowRAM is where Cloud Hypervisor's x86-64 guests stop below 4 GiB; the
// rest of their memory starts at 4 GiB and follows in the memory file.
const lowRAM = 3072 << 20

// punchFree asks the guest which page frames its allocator holds free and
// punches them out of the memory file the VM is mapped from. The guest
// zeroes every page it allocates (CONFIG_INIT_ON_ALLOC_DEFAULT_ON), so it
// never reads what a free frame held.
func (z *zz) punchFree(label string) {
	z.t.Helper()
	start := time.Now()
	bits, err := hex.DecodeString(z.guest("/usr/local/bin/zzmemcheck freemap"))
	if err != nil {
		z.t.Fatalf("free frame map: %v", err)
	}
	listed := time.Since(start)
	file := filepath.Join(z.e.path(z.key, snapshotDir), "memory-ranges")
	before := allocatedMiB(file)
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		z.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		z.t.Fatal(err)
	}
	size, fd := info.Size(), int(f.Fd())
	var free, punched int64
	calls := 0
	punch := func(off, end int64) {
		free += end - off
		for pos := off; pos < end; {
			data, seekErr := unix.Seek(fd, pos, unix.SEEK_DATA)
			if seekErr != nil || data >= end {
				return
			}
			hole, seekErr := unix.Seek(fd, data, unix.SEEK_HOLE)
			if seekErr != nil {
				z.t.Fatalf("seek the next hole: %v", seekErr)
			}
			hole = min(hole, end)
			if err = unix.Fallocate(fd, unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, data, hole-data); err != nil {
				z.t.Fatalf("punch %d+%d: %v", data, hole-data, err)
			}
			punched += hole - data
			calls++
			pos = hole
		}
	}
	frames := int64(len(bits)) * 8
	isFree := func(pfn int64) bool { return pfn < frames && bits[pfn/8]&(1<<(pfn%8)) != 0 }
	// ranges of the guest's physical memory and where each starts in the file
	for _, r := range [][3]int64{{0, min(lowRAM, size), 0}, {4 << 30, 4<<30 + max(size-lowRAM, 0), lowRAM}} {
		for pfn := r[0] / 4096; pfn < r[1]/4096; pfn++ {
			if !isFree(pfn) {
				continue
			}
			first := pfn
			for pfn < r[1]/4096 && isFree(pfn) {
				pfn++
			}
			punch(r[2]+first*4096-r[0], r[2]+pfn*4096-r[0])
		}
	}
	if err = f.Sync(); err != nil {
		z.t.Fatal(err)
	}
	z.logf("punchfree %s list_ms=%d punch_ms=%d guest_frames=%d free_mib=%d punched_mib=%d punch_calls=%d source_allocated_mib before=%d after=%d",
		label, listed.Milliseconds(), (time.Since(start) - listed).Milliseconds(), frames, free>>20, punched>>20, calls, before, allocatedMiB(file))
}

// privateConfig rewrites the saved VM configuration to private guest memory,
// which a copy-on-write restore needs.
func (z *zz) privateConfig() error {
	path := filepath.Join(z.e.path(z.key, snapshotDir), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var cfg map[string]any
	if err = dec.Decode(&cfg); err != nil {
		return err
	}
	memory, ok := cfg["memory"].(map[string]any)
	if !ok {
		return fmt.Errorf("saved configuration has no memory: %.200s", data)
	}
	if memory["shared"] == false {
		return nil
	}
	memory["shared"] = false
	if data, err = json.Marshal(cfg); err != nil {
		return err
	}
	z.logf("config rewritten to private memory")
	return os.WriteFile(path, data, 0o600)
}

func allocatedMiB(path string) int64 {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return -1
	}
	return st.Blocks * 512 >> 20
}

func zzFileSum(path string) (string, time.Duration) {
	start := time.Now()
	out, err := exec.Command("sha256sum", path).Output()
	if err != nil {
		return "error: " + err.Error(), time.Since(start)
	}
	return strings.Fields(string(out))[0], time.Since(start)
}

// digHoles punches every zero block out of a saved memory file whose
// monitor has exited, from a cold page cache, and checks that the file
// reads the same afterwards.
func (z *zz) digHoles(label, file string) {
	z.t.Helper()
	before := allocatedMiB(file)
	count, _, _, _ := extents(file)
	sum, sumTook := zzFileSum(file)
	unix.Sync()
	if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0); err != nil {
		z.t.Fatal(err)
	}
	start := time.Now()
	cmd := exec.Command("fallocate", "--dig-holes", file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		z.t.Fatalf("fallocate --dig-holes: %v: %s", err, out)
	}
	dug := time.Since(start)
	f, err := os.Open(file)
	if err == nil {
		err = errors.Join(f.Sync(), f.Close())
	}
	if err != nil {
		z.t.Fatal(err)
	}
	total := time.Since(start)
	after, _ := zzFileSum(file)
	if after != sum {
		z.t.Errorf("ZZ arm=%s fs=%s mib=%d CHECK-FAILED dig %s: the file reads differently after digging holes: %s, was %s", z.arm, z.fs, z.mib, label, after, sum)
	}
	countAfter, _, _, _ := extents(file)
	z.logf("dig %s cold_cache=true dig=%dms dig_and_fsync=%dms cpu_user=%dms cpu_sys=%dms allocated_mib before=%d after=%d extents before=%d after=%d content_same=%v hash_took=%dms",
		label, dug.Milliseconds(), total.Milliseconds(), cmd.ProcessState.UserTime().Milliseconds(), cmd.ProcessState.SystemTime().Milliseconds(),
		before, allocatedMiB(file), count, countAfter, after == sum, sumTook.Milliseconds())
}

// held is the host memory the unit holds as the design counts it: anonymous
// and shared memory, without the page cache of the mapped memory file.
func (z *zz) held() float64 {
	stat, _ := os.ReadFile(filepath.Join(z.e.cgroupRoot, runtime.SessionSlice, unitName(z.key), "memory.stat"))
	var total int64
	for line := range strings.SplitSeq(string(stat), "\n") {
		if name, v, _ := strings.Cut(line, " "); name == "anon" || name == "shmem" {
			n, _ := strconv.ParseInt(v, 10, 64)
			total += n
		}
	}
	return float64(total) / (1 << 20)
}

// guestUsed is the guest's memory that is not free, in MiB.
func (z *zz) guestUsed() int {
	var total, free int
	if _, err := fmt.Sscan(z.guest("awk '/^MemTotal:/{t=$2} /^MemFree:/{f=$2} END{print int(t/1024), int(f/1024)}' /proc/meminfo"), &total, &free); err != nil {
		z.t.Fatal(err)
	}
	return total - free
}

var nonzeroField = regexp.MustCompile(`nonzero=(\d+)`)

// fresh runs a checker command that allocates memory and fails the test if
// any new page did not read as zero.
func (z *zz) fresh(args string) string {
	z.t.Helper()
	out := z.call(args)
	if m := nonzeroField.FindStringSubmatch(out); m == nil || m[1] != "0" {
		z.t.Errorf("ZZ arm=%s fs=%s mib=%d CHECK-FAILED %s: new anonymous pages were not zero: %s", z.arm, z.fs, z.mib, args, out)
	}
	return out
}

// work is one cycle's guest load: the moving block goes to new pages, and
// another block is allocated, filled and freed. It logs what the host got
// back of the freed memory.
func (z *zz) work(label string, gen uint64) {
	blind := z.call("blind 256")
	before := z.held()
	moved := z.fresh(fmt.Sprintf("move %d", gen))
	for i := z.mainChunks; i < len(z.gens); i++ {
		z.gens[i] = gen
	}
	afterMove := z.held()
	transient := z.fresh(fmt.Sprintf("transient %d", z.transientMiB))
	peak := z.held()
	low := peak
	start := time.Now()
	for time.Since(start) < 20*time.Second {
		time.Sleep(2 * time.Second)
		if low = z.held(); low < peak-float64(z.transientMiB)*0.8 {
			break
		}
	}
	z.logf("work %s move=[%s] transient=[%s] held_mib before=%.0f after_move=%.0f after_transient=%.0f later=%.0f waited=%dms private_mib=%.0f blind_256=[%s]",
		label, moved, transient, before, afterMove, peak, low, time.Since(start).Milliseconds(), z.privateMiB(), blind)
}

// digBoot pauses a booted VM whose memory is shared, punches the zero pages
// out of its memory and resumes it.
func (z *zz) digBoot() {
	z.t.Helper()
	ctx := z.t.Context()
	api := newCHAPI(z.e.path(z.key, apiSocket))
	pid := z.chPID()
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		z.t.Fatal(err)
	}
	var memfds []string
	for _, fd := range fds {
		path := fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name())
		if target, _ := os.Readlink(path); strings.HasPrefix(target, "/memfd:") {
			var st unix.Stat_t
			if unix.Stat(path, &st) == nil && st.Size >= 1<<30 {
				memfds = append(memfds, path+"="+target)
			}
		}
	}
	if len(memfds) == 0 {
		z.t.Fatal("the monitor has no guest memory file")
	}
	before := z.held()
	start := time.Now()
	if err = api.pause(ctx); err != nil {
		z.t.Fatalf("pause: %v", err)
	}
	var out []byte
	var digErr error
	for _, memfd := range memfds {
		path, _, _ := strings.Cut(memfd, "=")
		if out, digErr = exec.Command("fallocate", "--dig-holes", path).CombinedOutput(); digErr != nil {
			break
		}
	}
	dug := time.Since(start)
	if err = api.resume(ctx); err != nil {
		z.t.Fatalf("resume: %v", err)
	}
	if digErr != nil {
		z.t.Fatalf("fallocate --dig-holes %v: %v: %s", memfds, digErr, out)
	}
	z.logf("bootdig files=%q paused_ms=%d dig_ms=%d held_mib before=%.0f after=%.0f", memfds, time.Since(start).Milliseconds(), dug.Milliseconds(), before, z.held())
}

func TestKVMZZFileBound(t *testing.T) {
	dir, kernel, server := os.Getenv("LAZY_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	arm := os.Getenv("LAZY_ARM")
	if dir == "" || arm == "cost" {
		t.Skip("set LAZY_DIR and a chain arm")
	}
	mib, err := strconv.Atoi(os.Getenv("LAZY_MIB"))
	if err != nil {
		t.Fatal(err)
	}
	cycles := 16
	if v := os.Getenv("LAZY_CYCLES"); v != "" {
		if cycles, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}
	zzShared, zzRestoreOpts, zzRestoreArgs, zzKeepSource = "on", ",memory_restore_mode=copyonwrite", []string{"-v"}, true
	if arm == "C" || arm == "D" || arm == "E" {
		zzCmdline = " init_on_free=1"
	}
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(Config{DataDir: dir, Images: images, Monitor: []string{server, MonitorCommand}, Helper: server, ProgramUnit: localBinProgram}, SystemdUnits{}, dialHelper)
	z := &zz{t: t, e: e, key: "run-zz-incr", fs: os.Getenv("LAZY_FS"), mib: mib, stat: os.Getenv("LAZY_DEV_STAT"), dump: os.Getenv("LAZY_DUMP_DIR"),
		arm: arm, dig: arm == "B" || arm == "C" || arm == "E",
		chain: sha256.Sum256([]byte("zzmemcheck")), rng: rand.New(rand.NewSource(int64(mib)))}
	t.Cleanup(func() { _ = e.destroy(context.Background(), z.key) })
	shmMiB := 128
	z.mainChunks, z.churnChunks, z.transientMiB, z.spikeMiB = 256, 256, 1024, 512
	if mib >= 8192 {
		shmMiB = 256
		z.mainChunks, z.churnChunks, z.spikeMiB = 2048, 1024, 3072
	}
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
	z.logf("boot took=%dms held_mib=%.0f", time.Since(start).Milliseconds(), z.held())
	if arm == "E" {
		z.digBoot()
		z.guest("true")
	}
	version, _ := exec.Command("cloud-hypervisor", "--version").Output()
	launched, _ := os.ReadFile(e.path(z.key, launchFile))
	z.logf("hypervisor %s memory_arg=%q", strings.TrimSpace(string(version)), regexp.MustCompile(`size=\d+M,shared=\w+`).Find(launched))
	z.logf("guest %s", zzGuestFacts(z.guest))
	z.logf("guest ram %s", z.guest("grep -i 'system ram' /proc/iomem | tr -s ' \\n' ' ;'; ls -l /proc/kpageflags 2>&1 | tr '\\n' ' '; zcat /proc/config.gz 2>/dev/null | grep -E 'INIT_ON|PROC_PAGE_MONITOR' | tr '\\n' ' '; true"))

	zzPush(t, e, z.key, os.Getenv("LAZY_MEMCHECK"), "cat >/usr/local/bin/zzmemcheck && chmod 755 /usr/local/bin/zzmemcheck")
	z.guest("setsid /usr/local/bin/zzmemcheck serve </dev/null >/var/log/zzmemcheck.log 2>&1 & sleep 1; test -S /run/zzmemcheck.sock")
	z.gens = make([]uint64, z.mainChunks+z.churnChunks)
	for i := range z.gens {
		z.gens[i] = 1
	}
	z.bootID = z.guest("cat /proc/sys/kernel/random/boot_id")
	z.shmSum = z.guest(fmt.Sprintf("mount -o remount,size=90%% /dev/shm; head -c %dM /dev/urandom >/dev/shm/fill; sha256sum </dev/shm/fill | cut -d' ' -f1", shmMiB))
	z.logf("fill %s pattern_mib=%d shm_mib=%d", z.fresh(fmt.Sprintf("fill %d %d 1", z.mainChunks, z.churnChunks)), len(z.gens), shmMiB)
	if _, err = fmt.Sscanf(z.call("status"), "pid=%s", &z.pid); err != nil {
		t.Fatal(err)
	}
	z.verify("after fill")
	// The boot's own high-water mark: a block is used and freed before the
	// first save, as in a session that built something before it went idle.
	z.work("0 first", 1)

	snapDir, nextDir := e.path(z.key, snapshotDir), e.path(z.key, "snapshot.next")
	// save settles the guest, snapshots it into a directory, ends the
	// monitor and, in the arms that do, digs the holes.
	save := func(n int, label, into string, dump bool, changed int) {
		z.settle(label)
		if arm == "G" && n > 0 {
			z.punchFree(label)
		}
		used, held, private := z.guestUsed(), z.held(), z.privateMiB()
		if err = os.Mkdir(into, 0o700); err != nil {
			t.Fatal(err)
		}
		fsBefore := z.fsUsed()
		res, saveErr := z.snapshot(label, into, dump, changed)
		if saveErr != nil {
			z.kernelLog("save " + label)
			t.Fatalf("save %s: %v%s", label, saveErr, e.logTail(z.key))
		}
		z.sleep()
		file := filepath.Join(into, "memory-ranges")
		written := allocatedMiB(file)
		if z.dig {
			z.digHoles(label, file)
			if dump {
				differing, pages, first, diffErr := diffPages(res.dump, file)
				if diffErr != nil || differing != 0 {
					t.Errorf("ZZ arm=%s fs=%s mib=%d CHECK-FAILED page compare after dig %s: %d of %d pages differ, first %v, %v", z.arm, z.fs, z.mib, label, differing, pages, first, diffErr)
				}
				z.logf("pagecompare-after-dig %s differing_pages=%d pages=%d err=%v", label, differing, pages, diffErr)
			}
		}
		if dump {
			_ = os.Remove(res.dump)
		}
		if into != snapDir {
			if err = errors.Join(os.RemoveAll(snapDir), os.Rename(into, snapDir)); err != nil {
				t.Fatal(err)
			}
		}
		z.syncfs()
		file = filepath.Join(snapDir, "memory-ranges")
		count, shared, _, _ := extents(file)
		z.logf("BOUND cycle=%d label=%q guest_used_mib=%d host_held_mib=%.0f private_mib=%.0f save_ms=%d dev_written_mib=%.0f file_mib_as_written=%d file_mib_at_rest=%d over_live_mib=%d extents=%d shared_extents=%d fs_used_mib before=%d at_rest=%d",
			n, label, used, held, private, (res.snapshot + res.fsync).Milliseconds(), float64(res.devBytes)/(1<<20), written, allocatedMiB(file),
			allocatedMiB(file)-int64(used), count, shared, fsBefore>>20, z.fsUsed()>>20)
	}
	save(0, "0 first", snapDir, false, 0)

	mid := cycles / 2
	for n := 1; n <= cycles; n++ {
		label := strconv.Itoa(n)
		compare := n == 1 || n == mid || n == cycles
		z.wake(label, compare)
		if !z.verify("after wake " + label) {
			t.Fatalf("cycle %d: the guest check failed%s", n, e.logTail(z.key))
		}
		if z.spiked {
			out := z.call("unspike")
			if !strings.Contains(out, "bad=0 ") {
				t.Errorf("ZZ arm=%s fs=%s mib=%d CHECK-FAILED unspike %s: %s", z.arm, z.fs, z.mib, label, out)
			}
			z.logf("unspike %s %s", label, out)
			z.spiked = false
		}
		z.rewrite(uint64(n+1), 64)
		z.work(label, uint64(n+1))
		if n == 6 {
			z.logf("spike %s spike_mib=%d %s", label, z.spikeMiB, z.fresh(fmt.Sprintf("spike %d", z.spikeMiB)))
			z.spiked = true
			label += " spike held"
		}
		save(n, label, nextDir, compare, 64+z.churnChunks)
	}

	z.wake("final", false)
	if !z.verify("after wake final") {
		t.Fatalf("the guest check failed after the last save%s", e.logTail(z.key))
	}
	z.logf("final nonzero check: %s", z.fresh("transient 64"))
}

// zzGuestFacts says how the guest kernel was booted and whether its balloon
// negotiated free page reporting (feature bit 5) or page poison (bit 4).
func zzGuestFacts(guest func(string) string) string {
	return guest(`printf 'cmdline_init_on_free=%s ' "$(grep -o 'init_on_free=[0-9]' /proc/cmdline || echo unset)"
for d in /sys/bus/virtio/devices/*; do
	[ "$(cat "$d/device")" = 0x0005 ] || continue
	f=$(cat "$d/features")
	printf 'balloon=%s reporting_negotiated=%s page_poison_negotiated=%s ' "${d##*/}" "$(printf %s "$f" | cut -c6)" "$(printf %s "$f" | cut -c5)"
done
printf 'reporting_order=%s ' "$(cat /sys/module/page_reporting/parameters/page_reporting_order 2>/dev/null)"
grep -E '^(MemTotal|MemFree|SwapTotal):' /proc/meminfo | tr -s ' \n' ' '`)
}

// zzPush pipes a host file into a guest command's standard input.
func zzPush(t *testing.T, e *engine, key, path, script string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, att, err := e.spawn(t.Context(), key, guest.ProcessSpec{ID: processID("exec"), Argv: []string{"/bin/sh", "-c", script}, Lossless: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(att.Stdin(), f); err != nil {
		t.Fatal(err)
	}
	if err = p.CloseStdin(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code, waitErr := p.Wait(t.Context()); code != 0 || waitErr != nil {
		t.Fatalf("push %s into the guest: %d, %v", path, code, waitErr)
	}
	_ = att.Close()
}

// TestKVMZZInitOnFreeCost times a build inside the guest with and without
// init_on_free=1, on alternating boots of one runner.
//
//	LAZY_GOROOT   gzip tarball of a Go toolchain
func TestKVMZZInitOnFreeCost(t *testing.T) {
	dir, kernel, server := os.Getenv("LAZY_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	if dir == "" || os.Getenv("LAZY_ARM") != "cost" {
		t.Skip("set LAZY_ARM=cost")
	}
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(Config{DataDir: dir, Images: images, Monitor: []string{server, MonitorCommand}, Helper: server, ProgramUnit: localBinProgram}, SystemdUnits{}, dialHelper)
	key := "run-zz-cost"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	guestRun := func(script string) string {
		out, code := vmRun(t, e, key, script)
		if code != 0 {
			t.Fatalf("%q exited %d: %s%s", script, code, out, e.logTail(key))
		}
		return strings.TrimSpace(out)
	}
	cpu := func() int64 {
		data, _ := os.ReadFile(filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(key), "cpu.stat"))
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
				n, _ := strconv.ParseInt(v, 10, 64)
				return n / 1000
			}
		}
		return -1
	}
	const env = "export PATH=/opt/go/bin:$PATH GOROOT=/opt/go GOCACHE=/tmp/zzcache GOPATH=/tmp/zzgopath GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS= HOME=/root; TIMEFORMAT='real=%R user=%U sys=%S'; "
	benches := []struct{ name, script string }{
		{"go-build-std-cmd", "rm -rf /tmp/zzcache; cd /opt/go/src && time go build -a std cmd"},
		{"untar-and-delete", "time (mkdir /tmp/zzcopy && tar -cf - -C /opt go | tar -xf - -C /tmp/zzcopy && rm -rf /tmp/zzcopy)"},
		{"alloc-fill-free-1g", "time /usr/local/bin/zzmemcheck call transient 1024"},
	}
	held := func() int64 {
		stat, _ := os.ReadFile(filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(key), "memory.stat"))
		var total int64
		for line := range strings.SplitSeq(string(stat), "\n") {
			if name, v, _ := strings.Cut(line, " "); name == "anon" || name == "shmem" {
				n, _ := strconv.ParseInt(v, 10, 64)
				total += n
			}
		}
		return total >> 20
	}
	// today: reporting on; zeroed: init_on_free=1, which makes the guest
	// refuse reporting; unreported: today's kernel arguments on a balloon
	// that offers no reporting, to tell the zeroing from the reporting.
	for round := range 9 {
		iof := []string{"today", "zeroed", "unreported"}[round%3]
		zzShared, zzCmdline, zzReporting = "on", "", "on"
		switch iof {
		case "zeroed":
			zzCmdline = " init_on_free=1"
		case "unreported":
			zzReporting = "off"
		}
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 2, MemoryLimitBytes: 8192 << 20,
		}
		if _, err = e.create(t.Context(), key, spec, options{}); err != nil {
			t.Fatal(err)
		}
		if err = e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		t.Logf("ZZ cost round=%d guest_kind=%v guest %s", round, iof, zzGuestFacts(guestRun))
		zzPush(t, e, key, os.Getenv("LAZY_MEMCHECK"), "cat >/usr/local/bin/zzmemcheck && chmod 755 /usr/local/bin/zzmemcheck")
		guestRun("setsid /usr/local/bin/zzmemcheck serve </dev/null >/var/log/zzmemcheck.log 2>&1 & sleep 1; test -S /run/zzmemcheck.sock")
		start := time.Now()
		zzPush(t, e, key, os.Getenv("LAZY_GOROOT"), "mkdir -p /opt/go && tar -xzf - -C /opt/go")
		t.Logf("ZZ cost round=%d toolchain copied in %s: %s; %s", round, time.Since(start).Round(time.Second), guestRun(env+"go version"), guestRun("df -h / /tmp | tr -s ' \\n' ' '; nproc"))
		for rep := range 3 {
			for _, b := range benches {
				cpu0, wall := cpu(), time.Now()
				out := guestRun("bash -c " + shellQuote(env+b.script) + " 2>&1 | tail -3 | tr '\\n' ' '")
				t.Logf("ZZ COST round=%d guest_kind=%v bench=%s rep=%d host_wall_ms=%d unit_cpu_ms=%d guest=[%s]", round, iof, b.name, rep, time.Since(wall).Milliseconds(), cpu()-cpu0, out)
			}
		}
		guestRun("rm -rf /tmp/zzcache; sync; echo 3 >/proc/sys/vm/drop_caches")
		after := held()
		time.Sleep(40 * time.Second)
		t.Logf("ZZ HELD round=%d guest_kind=%v host_held_mib after_benches=%d 40s_later=%d guest_used_mib=%s", round, iof, after, held(),
			guestRun("awk '/^MemTotal:/{t=$2} /^MemFree:/{f=$2} END{print int((t-f)/1024)}' /proc/meminfo"))
		if err = e.destroy(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
