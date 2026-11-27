//go:build kvm

package vm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// Scratch experiment ch-first-save-cost, never merged: what the first save
// after a cold boot costs when guest memory is private.
//
//	P  shared=on boot, save, eager copy restore (today)
//	Q  shared=off boot, save, copy-on-write restore
//	R  shared=on boot and save; restore with the saved configuration
//	   rewritten to private memory, copy-on-write; run; save again; restore
//	S  Q with fallocate --dig-holes on memory-ranges before the fsync
//
// Round 0 of each arm is a guest with nothing but the dropped page cache,
// for the size bound; the others add a 1 GiB /dev/shm file and a 256 MiB
// pattern in a process's memory, which a wake must bring back.

const (
	lazyCOW     = ",memory_restore_mode=copyonwrite"
	lazyPage    = 4096
	lazyPattern = `import mmap, os, struct, time
N = 256 << 20
P = 4096
m = mmap.mmap(-1, N)
def page(g, i):
    return struct.pack('<QQ', g, i) * 256
def fill(g):
    for i in range(N // P):
        m[i * P:(i + 1) * P] = page(g, i)
def check(g):
    bad = 0
    for i in range(N // P):
        if m[i * P:(i + 1) * P] != page(g, i):
            bad += 1
    return bad
g = 1
fill(g)
tick = 0
with open('/tmp/pat.ready.tmp', 'w') as f:
    f.write(str(os.getpid()))
os.rename('/tmp/pat.ready.tmp', '/tmp/pat.ready')
while True:
    tick += 1
    with open('/dev/shm/tick.tmp', 'w') as f:
        f.write(str(tick))
    os.rename('/dev/shm/tick.tmp', '/dev/shm/tick')
    if os.path.exists('/tmp/pat.cmd'):
        c = open('/tmp/pat.cmd').read().strip()
        if c == 'bump':
            g += 1
            fill(g)
        r = 'gen=%d bad=%d' % (g, check(g))
        with open('/tmp/pat.res.tmp', 'w') as f:
            f.write(r + '\n')
        os.remove('/tmp/pat.cmd')
        os.rename('/tmp/pat.res.tmp', '/tmp/pat.res')
    time.sleep(0.1)
`
)

func lazyPatternCmd(cmd string) string {
	return "rm -f /tmp/pat.res; echo " + cmd + " >/tmp/pat.c; mv /tmp/pat.c /tmp/pat.cmd; i=0; " +
		"while [ ! -e /tmp/pat.res ] && [ $i -lt 1800 ]; do sleep 0.1; i=$((i+1)); done; cat /tmp/pat.res"
}

type lazyRound struct {
	t       *testing.T
	h       *checkoutHost
	arm     string
	kind    string
	gib     int
	round   int
	bare    bool
	compare bool
	dump    string
	key     string
	tag     string

	bootID, pid, sha string
	gen              int
	tick             uint64
}

func lazyEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func lazyFS(path string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return err.Error()
	}
	switch uint64(st.Type) {
	case 0x58465342:
		return "xfs"
	case 0xEF53:
		return "ext4"
	}
	return fmt.Sprintf("%#x", uint64(st.Type))
}

func lazyRead(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(string(data))
}

func TestKVMZZLazyFirstSave(t *testing.T) {
	gib := lazyEnvInt("AETHER_LAZY_GIB", 2)
	rounds := lazyEnvInt("AETHER_LAZY_ROUNDS", 5)
	arms := strings.Split(cmp.Or(os.Getenv("AETHER_LAZY_ARMS"), "P,Q,R,S"), ",")
	dump := os.Getenv("AETHER_LAZY_DUMP_DIR")
	h := newCheckoutHost(t)
	if dump != "" {
		if err := os.MkdirAll(dump, 0o700); err != nil {
			t.Fatal(err)
		}
		lazy.reveal = dump
	}
	version, _ := exec.Command("cloud-hypervisor", "--version").CombinedOutput()
	fmt.Printf("LAZY-ENV gib=%d rounds=%d arms=%s ch=%q engine_dir=%s engine_fs=%s dump_dir=%s dump_fs=%s kernel=%q thp=%q shmem_thp=%q use_zero_page=%s meminfo=%q\n",
		gib, rounds, strings.Join(arms, ","), strings.TrimSpace(string(version)), h.e.root, lazyFS(h.e.root), dump, lazyFS(cmp.Or(dump, "/")),
		lazyRead("/proc/sys/kernel/osrelease"), lazyRead("/sys/kernel/mm/transparent_hugepage/enabled"),
		lazyRead("/sys/kernel/mm/transparent_hugepage/shmem_enabled"), lazyRead("/sys/kernel/mm/transparent_hugepage/use_zero_page"), strings.Join(strings.Fields(strings.SplitN(lazyRead("/proc/meminfo"), "\n", 2)[0]), " "))

	ws := domain.WorkspaceID("wslazy")
	base := h.workspace(t, ws, 20)
	failed := 0
	for round := 0; round <= rounds; round++ {
		for _, arm := range arms {
			r := &lazyRound{
				t: t, h: h, arm: arm, kind: arm[:1], gib: gib, round: round, bare: round == 0, dump: dump,
				compare: dump != "" && (gib <= 2 || round <= 1),
				key:     fmt.Sprintf("run-lz%s%d-%d-%x", strings.ToLower(arm), gib, round, time.Now().Unix()&0xfffff),
			}
			r.tag = fmt.Sprintf("arm=%s gib=%d round=%d bare=%t", arm, gib, round, r.bare)
			start := time.Now()
			err := r.run(ws, base)
			if err != nil {
				failed++
				kernel, _ := exec.Command("sh", "-c", "dmesg | grep -iE 'oom|out of memory|killed process' | tail -8").CombinedOutput()
				fmt.Printf("LAZY FAIL %s took_s=%.0f err=%q log=%q dmesg=%q\n", r.tag, time.Since(start).Seconds(), err.Error(), h.e.logTail(r.key), kernel)
			} else {
				fmt.Printf("LAZY ROUND %s ok=true took_s=%.0f\n", r.tag, time.Since(start).Seconds())
			}
			lazy.unitExtraMiB = 0
			lazy.shared, lazy.restoreMode, lazy.beforePause, lazy.afterSnapshot, lazy.afterSync = "", "", nil, nil, nil
			_ = h.e.destroy(context.Background(), r.key)
			_ = h.git.RemoveRunHistory(context.Background(), domain.RunID(r.key))
		}
	}
	fmt.Printf("LAZY DONE gib=%d failed_rounds=%d\n", gib, failed)
	if failed > 0 {
		t.Errorf("%d rounds failed", failed)
	}
}

func (r *lazyRound) sh(timeout time.Duration, script string) (string, error) {
	ctx, cancel := context.WithTimeout(r.t.Context(), timeout)
	defer cancel()
	code, out, errOut, err := r.h.e.exec(ctx, r.key, []string{"/bin/sh", "-c", script}, "")
	if err != nil || code != 0 {
		return out, fmt.Errorf("guest command %.60q: exit %d, %v: %s%s", script, code, err, out, errOut)
	}
	return strings.TrimSpace(out), nil
}

func (r *lazyRound) run(ws domain.WorkspaceID, base string) error {
	e, ctx := r.h.e, r.t.Context()
	if r.kind == "Q" || r.kind == "S" {
		lazy.shared = "off"
	}
	// An X arm's unit may hold the guest's memory twice over.
	if len(r.arm) > 1 {
		lazy.unitExtraMiB = guestMiB(int64(r.gib) << 10)
	}
	checkout, _, err := r.h.git.CreateRunCheckoutAt(ctx, ws, domain.RunID(r.key), base, "main", "a task", "")
	if err != nil {
		return err
	}
	spec := runtime.Spec{
		Image: os.Getenv("AETHER_TEST_IMAGE"), CreationKey: r.key, TTY: true, User: "0:0",
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env:     map[string]string{"AETHER_RUN_ID": r.key, "HOME": "/root"}, CPULimit: 2, MemoryLimitBytes: int64(r.gib) << 30,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
	}
	start := time.Now()
	if _, err = e.create(ctx, r.key, spec, options{}); err != nil {
		return err
	}
	created := time.Since(start)
	if err = e.start(ctx, r.key); err != nil {
		return err
	}
	booted := time.Since(start)
	if _, err = r.sh(10*time.Minute, "head -c 1400M /dev/urandom >/var/tmp/f && sync"); err != nil {
		return err
	}
	if r.bare {
		_, err = r.sh(time.Minute, "setsid sleep 4000 </dev/null >/dev/null 2>&1 & echo $! >/tmp/pat.ready")
	} else {
		if _, err = r.sh(5*time.Minute, "cat >/tmp/pat.py <<'EOF'\n"+lazyPattern+"EOF\n"+
			"setsid python3 /tmp/pat.py </dev/null >/tmp/pat.log 2>&1 &\n"+
			"i=0; while [ ! -e /tmp/pat.ready ] && [ $i -lt 1800 ]; do sleep 0.1; i=$((i+1)); done; cat /tmp/pat.ready >/dev/null || { cat /tmp/pat.log; exit 1; }"); err != nil {
			return err
		}
		r.gen = 1
		r.sha, err = r.sh(10*time.Minute, "head -c 1024M /dev/urandom >/dev/shm/fill && sha256sum </dev/shm/fill | cut -d' ' -f1")
	}
	if err != nil {
		return err
	}
	out, err := r.sh(time.Minute, "cat /proc/sys/kernel/random/boot_id /tmp/pat.ready; echo; free -m | awk '/^Mem:/ {print $3, $6}'")
	if err != nil {
		return err
	}
	fields := strings.Fields(out)
	if len(fields) != 4 {
		return fmt.Errorf("guest state %q", out)
	}
	r.bootID, r.pid = fields[0], fields[1]
	fmt.Printf("LAZY BOOT %s shared=%s unit_max_mib=%d create_ms=%d boot_ms=%d guest_used_mib=%s guest_cache_mib=%s\n",
		r.tag, cmp.Or(lazy.shared, "on"), lazyUint(filepath.Join(r.cgroup(), "memory.max"))>>20, created.Milliseconds(), booted.Milliseconds(), fields[2], fields[3])

	if err = r.save(1); err != nil {
		return err
	}
	mode := lazyCOW
	switch r.kind {
	case "P":
		mode = ""
	case "Q":
		if r.bare {
			if err = r.truncated(); err != nil {
				return err
			}
		}
	case "R":
		if err = r.privateConfig(); err != nil {
			return err
		}
	}
	if err = r.wake(1, mode); err != nil {
		return err
	}
	if r.kind != "R" {
		return nil
	}
	if !r.bare {
		if out, err = r.sh(5*time.Minute, lazyPatternCmd("bump")); err != nil {
			return err
		}
		r.gen = 2
		if out != "gen=2 bad=0" {
			return fmt.Errorf("pattern after the bump: %q", out)
		}
	}
	if err = r.save(2); err != nil {
		return err
	}
	return r.wake(2, lazyCOW)
}

func (r *lazyRound) cgroup() string {
	return filepath.Join(r.h.e.cgroupRoot, runtime.SessionSlice, unitName(r.key))
}

func lazyUint(path string) uint64 {
	v, _ := strconv.ParseUint(lazyRead(path), 10, 64)
	return v
}

func lazyStats(cgroup string) map[string]uint64 {
	got := map[string]uint64{}
	for _, line := range strings.Split(lazyRead(filepath.Join(cgroup, "memory.stat")), "\n") {
		if name, v, ok := strings.Cut(line, " "); ok {
			got[name], _ = strconv.ParseUint(v, 10, 64)
		}
	}
	return got
}

// lazyStat is anon, file and shmem of a cgroup's memory.stat, in MiB.
func lazyStat(cgroup string) string {
	got := lazyStats(cgroup)
	return fmt.Sprintf("%d/%d/%d", got["anon"]>>20, got["file"]>>20, got["shmem"]>>20)
}

func (r *lazyRound) hypervisor() string {
	for _, pid := range strings.Fields(lazyRead(filepath.Join(r.cgroup(), "cgroup.procs"))) {
		if strings.HasPrefix(lazyRead("/proc/"+pid+"/comm"), "cloud-hyperv") {
			return pid
		}
	}
	return ""
}

func (r *lazyRound) written() uint64 {
	for _, line := range strings.Split(lazyRead("/proc/"+r.hypervisor()+"/io"), "\n") {
		if v, ok := strings.CutPrefix(line, "wchar: "); ok {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

// chLog is what Cloud Hypervisor said about a restore or snapshot.
func (r *lazyRound) chLog() string {
	var said []string
	for _, line := range strings.Split(lazyRead(r.h.e.path(r.key, monitorLog)), "\n") {
		if strings.Contains(line, "Restore (mode") || strings.Contains(line, "Snapshot:") || strings.Contains(line, "falling back") {
			said = append(said, line)
		}
	}
	return strings.Join(said, " | ")
}

func (r *lazyRound) save(n int) error {
	e, cg := r.h.e, r.cgroup()
	if !r.bare {
		tick, err := r.sh(time.Minute, "cat /dev/shm/tick")
		if err != nil {
			return err
		}
		r.tick, _ = strconv.ParseUint(tick, 10, 64)
	}
	var (
		peak                        *os.File
		reset                       bool
		current, peakSnap, peakSync uint64
		wrote                       uint64
		statBefore, statAfter       string
		compared, said              string
		// Sampled every 50 ms from the pause on: the most the unit held,
		// the most of it that was guest memory (anon and shmem), and the
		// last of what it read.
		maxCurrent, maxGuest uint64
		lastStat, events     string
		stop, done           = make(chan struct{}), make(chan struct{})
		sampling             bool
	)
	held := lazyUint(filepath.Join(cg, "memory.current"))
	readPeak := func() uint64 {
		if peak == nil {
			return 0
		}
		buf := make([]byte, 64)
		got, _ := peak.ReadAt(buf, 0)
		v, _ := strconv.ParseUint(strings.TrimSpace(string(buf[:got])), 10, 64)
		return v
	}
	lazy.beforePause = func(string) {
		current = lazyUint(filepath.Join(cg, "memory.current"))
		statBefore = lazyStat(cg)
		wrote = r.written()
		// A write resets the peak this descriptor reads to the current use.
		var err error
		if peak, err = os.OpenFile(filepath.Join(cg, "memory.peak"), os.O_RDWR, 0); err == nil {
			_, err = peak.WriteString("reset")
			reset = err == nil
		}
		sampling = true
		go func() {
			defer close(done)
			for {
				if stat := lazyStats(cg); len(stat) > 0 {
					maxGuest = max(maxGuest, stat["anon"]+stat["shmem"])
					maxCurrent = max(maxCurrent, lazyUint(filepath.Join(cg, "memory.current")))
					lastStat = fmt.Sprintf("anon=%d file=%d shmem=%d dirty=%d writeback=%d", stat["anon"]>>20, stat["file"]>>20, stat["shmem"]>>20, stat["file_dirty"]>>20, stat["file_writeback"]>>20)
					events = strings.Join(strings.Fields(lazyRead(filepath.Join(cg, "memory.events"))), " ")
				}
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}()
	}
	sampled := func() {
		if sampling {
			sampling = false
			close(stop)
			<-done
		}
	}
	var dig time.Duration
	lazy.afterSnapshot = func(_, dir string) error {
		peakSnap = readPeak()
		wrote = r.written() - wrote
		if r.kind != "S" {
			return nil
		}
		start := time.Now()
		out, err := exec.Command("fallocate", "--dig-holes", filepath.Join(dir, "memory-ranges")).CombinedOutput()
		dig = time.Since(start)
		if err != nil {
			return fmt.Errorf("fallocate --dig-holes: %w: %s", err, out)
		}
		return nil
	}
	lazy.afterSync = func(_, dir string, api chAPI) {
		peakSync = readPeak()
		statAfter = lazyStat(cg)
		sampled()
		said = r.chLog()
		if r.compare {
			compared = r.dumpCompare(n, dir, api)
		}
	}
	defer func() {
		lazy.beforePause, lazy.afterSnapshot, lazy.afterSync = nil, nil, nil
		if peak != nil {
			_ = peak.Close()
		}
	}()
	start := time.Now()
	if err := e.save(r.t.Context(), r.key); err != nil {
		sampled()
		fmt.Printf("LAZY SAVEFAIL %s n=%d after_ms=%d current_mib=%d max_current_mib=%d max_guest_mib=%d before=%s last=%q events=%q\n",
			r.tag, n, time.Since(start).Milliseconds(), current>>20, maxCurrent>>20, maxGuest>>20, statBefore, lastStat, events)
		return fmt.Errorf("save %d: %w", n, err)
	}
	total := time.Since(start)
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(e.path(r.key, snapshotDir), "memory-ranges"), &st); err != nil {
		return err
	}
	fmt.Printf("LAZY SAVE %s n=%d total_ms=%d sync1_ms=%d report_ms=%d sync2_ms=%d pause_ms=%d snapshot_ms=%d dig_ms=%d fsync_ms=%d "+
		"apparent_mib=%d alloc_mib=%d ch_wrote_mib=%d held_at_start_mib=%d current_mib=%d peak_reset=%t peak_snapshot_mib=%d peak_fsync_mib=%d "+
		"anon_file_shmem_before=%s anon_file_shmem_after=%s max_current_mib=%d max_guest_mib=%d last=%q events=%q chlog=%q\n",
		r.tag, n, total.Milliseconds(), lazy.sync1.Milliseconds(), lazy.report.Milliseconds(), lazy.sync2.Milliseconds(), lazy.pause.Milliseconds(),
		lazy.snapshot.Milliseconds(), dig.Milliseconds(), (lazy.fsync).Milliseconds(),
		st.Size>>20, st.Blocks*512>>20, wrote>>20, held>>20, current>>20, reset, peakSnap>>20, peakSync>>20, statBefore, statAfter, maxCurrent>>20, maxGuest>>20, lastStat, events, said)
	if compared != "" {
		fmt.Printf("LAZY CMP %s n=%d %s\n", r.tag, n, compared)
	}
	return nil
}

// dumpCompare has the paused VM write all of its memory once more, to the
// dump directory on another filesystem, and counts the pages in which the
// save's memory file differs from it.
func (r *lazyRound) dumpCompare(n int, dir string, api chAPI) string {
	dst := filepath.Join(r.dump, fmt.Sprintf("%s-%d", r.key, n))
	if err := os.Mkdir(dst, 0o700); err != nil {
		return "error=" + strconv.Quote(err.Error())
	}
	defer func() { _ = os.RemoveAll(dst) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	start := time.Now()
	if err := api.snapshot(ctx, dst); err != nil {
		return "error=" + strconv.Quote(err.Error())
	}
	dumped := time.Since(start)
	start = time.Now()
	pages, differ, nonzero, err := lazyCompare(filepath.Join(dir, "memory-ranges"), filepath.Join(dst, "memory-ranges"))
	var st unix.Stat_t
	_ = unix.Stat(filepath.Join(dst, "memory-ranges"), &st)
	return fmt.Sprintf("pages=%d differ=%d dump_nonzero_pages=%d dump_nonzero_mib=%d dump_alloc_mib=%d dump_fs=%s dump_ms=%d compare_ms=%d error=%q",
		pages, differ, nonzero, nonzero*lazyPage>>20, st.Blocks*512>>20, lazyFS(dst), dumped.Milliseconds(), time.Since(start).Milliseconds(), fmt.Sprint(err))
}

func lazyCompare(a, b string) (pages, differ, nonzero uint64, err error) {
	fa, err := os.Open(a)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = fa.Close() }()
	fb, err := os.Open(b)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = fb.Close() }()
	ia, _ := fa.Stat()
	ib, _ := fb.Stat()
	if ia.Size() != ib.Size() {
		return 0, 0, 0, fmt.Errorf("sizes differ: %d and %d bytes", ia.Size(), ib.Size())
	}
	const chunk = 4 << 20
	bufA, bufB, zero := make([]byte, chunk), make([]byte, chunk), make([]byte, chunk)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb {
			return pages, differ, nonzero, fmt.Errorf("short read: %d and %d bytes (%v, %v)", na, nb, errA, errB)
		}
		same, empty := bytes.Equal(bufA[:na], bufB[:nb]), bytes.Equal(bufB[:nb], zero[:nb])
		for off := 0; off < na; off += lazyPage {
			end := min(off+lazyPage, na)
			pages++
			if !same && !bytes.Equal(bufA[off:end], bufB[off:end]) {
				differ++
			}
			if !empty && !bytes.Equal(bufB[off:end], zero[off:end]) {
				nonzero++
			}
		}
		if errA != nil {
			if errors.Is(errA, io.EOF) || errors.Is(errA, io.ErrUnexpectedEOF) {
				return pages, differ, nonzero, nil
			}
			return pages, differ, nonzero, errA
		}
	}
}

// truncated is the failing control: a memory file 4 KiB short must be
// refused by a copy-on-write restore. The file is then made whole again.
func (r *lazyRound) truncated() error {
	e := r.h.e
	path := filepath.Join(e.path(r.key, snapshotDir), "memory-ranges")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	tail := make([]byte, lazyPage)
	if _, err = f.ReadAt(tail, info.Size()-lazyPage); err != nil {
		return err
	}
	if err = f.Truncate(info.Size() - lazyPage); err != nil {
		return err
	}
	lazy.restoreMode = lazyCOW
	start := time.Now()
	restoreErr := e.restore(r.t.Context(), r.key)
	up, _ := e.running(r.t.Context(), r.key)
	fmt.Printf("LAZY TRUNC %s rejected=%t still_running=%t took_ms=%d err=%q log=%q\n", r.tag, restoreErr != nil, up, time.Since(start).Milliseconds(), fmt.Sprint(restoreErr), e.logTail(r.key))
	if restoreErr == nil {
		return errors.New("a copy-on-write restore accepted a memory file 4 KiB short")
	}
	if err = f.Truncate(info.Size()); err != nil {
		return err
	}
	if _, err = f.WriteAt(tail, info.Size()-lazyPage); err != nil {
		return err
	}
	return f.Sync()
}

// privateConfig rewrites the saved VM configuration to private guest memory.
func (r *lazyRound) privateConfig() error {
	path := filepath.Join(r.h.e.path(r.key, snapshotDir), "config.json")
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
	was, _ := json.Marshal(memory)
	memory["shared"] = false
	if data, err = json.Marshal(cfg); err != nil {
		return err
	}
	now, _ := json.Marshal(memory)
	fmt.Printf("LAZY CFG %s memory_was=%q memory_now=%q\n", r.tag, was, now)
	return os.WriteFile(path, data, 0o600)
}

func (r *lazyRound) wake(n int, mode string) error {
	e := r.h.e
	lazy.restoreMode = mode
	unix.Sync()
	if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0); err != nil {
		return err
	}
	start := time.Now()
	if err := e.restore(r.t.Context(), r.key); err != nil {
		return fmt.Errorf("restore %d: %w", n, err)
	}
	restored := time.Since(start)
	boot, err := r.sh(3*time.Minute, "cat /proc/sys/kernel/random/boot_id")
	first := time.Since(start)
	if err != nil {
		return fmt.Errorf("first command after restore %d (restore took %s, waited %s): %w", n, restored, first, err)
	}
	mapped := strings.Contains(lazyRead("/proc/"+r.hypervisor()+"/maps"), "memory-ranges")
	fmt.Printf("LAZY WAKE %s n=%d mode=%s restore_ms=%d first_command_ms=%d memory_file_mapped=%t unit_current_mib=%d anon_file_shmem=%s chlog=%q\n",
		r.tag, n, cmp.Or(strings.TrimPrefix(mode, ",memory_restore_mode="), "copy"), restored.Milliseconds(), first.Milliseconds(), mapped,
		lazyUint(filepath.Join(r.cgroup(), "memory.current"))>>20, lazyStat(r.cgroup()), r.chLog())
	if boot != r.bootID {
		return fmt.Errorf("wake %d: boot_id %q, want %q", n, boot, r.bootID)
	}
	script := "cat /proc/sys/kernel/random/boot_id; pid=$(cat /tmp/pat.ready); kill -0 $pid && echo $pid"
	want := []string{r.bootID, r.pid}
	if !r.bare {
		script += "; a=$(cat /dev/shm/tick); sleep 1; b=$(cat /dev/shm/tick); echo $a $b; " + lazyPatternCmd("check") + "; sha256sum </dev/shm/fill | cut -d' ' -f1"
	}
	start = time.Now()
	out, err := r.sh(10*time.Minute, script)
	if err != nil {
		return fmt.Errorf("check after wake %d: %w", n, err)
	}
	got := strings.Fields(out)
	ok := len(got) >= 2 && got[0] == want[0] && got[1] == want[1]
	if !r.bare {
		ok = ok && len(got) == 7
		if ok {
			a, _ := strconv.ParseUint(got[2], 10, 64)
			b, _ := strconv.ParseUint(got[3], 10, 64)
			ok = a >= r.tick && b > a && got[4] == fmt.Sprintf("gen=%d", r.gen) && got[5] == "bad=0" && got[6] == r.sha
		}
	}
	fmt.Printf("LAZY CHECK %s n=%d ok=%t check_ms=%d got=%q want_boot=%s want_pid=%s want_tick_from=%d want_gen=%d want_sha=%s unit_current_mib=%d\n",
		r.tag, n, ok, time.Since(start).Milliseconds(), out, r.bootID, r.pid, r.tick, r.gen, r.sha, lazyUint(filepath.Join(r.cgroup(), "memory.current"))>>20)
	if !ok {
		return fmt.Errorf("check after wake %d: got %q", n, out)
	}
	return nil
}
