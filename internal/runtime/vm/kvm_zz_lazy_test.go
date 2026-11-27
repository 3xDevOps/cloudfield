//go:build kvm

package vm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// Experiment ch-wake-speed-and-memory (scratch/lazy-*): a measurement, not
// a product test. Every result is a line that starts with LAZY.

const (
	lazyV55   = "/usr/local/bin/cloud-hypervisor-v55"
	lazyCOW   = ",memory_restore_mode=copyonwrite"
	lazyMagic = 0x5aa50000
	lazyKids  = 3
)

// lazyTree is the session's main process: lazyKids children that each hold
// 100 MiB of anonymous memory, every page stamped with (child, generation,
// page) over random bytes, and a parent that counts a heartbeat. A child
// that sees a new generation in ctl/gen checks every page, body included,
// against the old one, stamps the new one and reports how many were wrong.
const lazyTree = `
import os, struct, time
CTL = '/dev/shm/ctl'; PG = 4096; MB = 100; BLK = 1 << 20
def put(name, text):
    with open(CTL + '/' + name + '.tmp', 'w') as f: f.write(text)
    os.rename(CTL + '/' + name + '.tmp', CTL + '/' + name)
def child(i):
    block = os.urandom(BLK)
    buf = bytearray(BLK * MB)
    for k in range(MB): buf[k * BLK:(k + 1) * BLK] = block
    view, ref = memoryview(buf), memoryview(block)
    pages, gen = len(buf) // PG, 0
    for p in range(pages): struct.pack_into('<III', buf, p * PG, 0x5aa50000 + i, gen, p)
    put('done.%d' % i, '0 0\n')
    while True:
        blind = False
        try:
            parts = open(CTL + '/gen').read().split(); want = int(parts[0]); blind = len(parts) > 1
        except Exception: want = gen
        if want != gen:
            bad = 0
            for p in range(pages):
                off = p * PG; b = off % BLK
                if blind: pass
                elif struct.unpack_from('<III', buf, off) != (0x5aa50000 + i, gen, p) or view[off + 12:off + PG] != ref[b + 12:b + PG]: bad += 1
                struct.pack_into('<III', buf, off, 0x5aa50000 + i, want, p)
            gen = want
            put('done.%d' % i, '%d %d\n' % (gen, bad))
        time.sleep(0.02)
os.makedirs(CTL, exist_ok=True)
pids = []
for i in range(3):
    pid = os.fork()
    if pid == 0: child(i)
    pids.append(pid)
put('pids', ' '.join('%d:%s' % (p, open('/proc/%d/stat' % p).read().rsplit(')', 1)[1].split()[19]) for p in pids) + '\n')
n = 0
while True:
    n += 1; put('hb', '%d\n' % n); time.sleep(0.2)
`

const lazyBump = `g=$1; echo $g $2 >/dev/shm/ctl/gen.tmp && mv /dev/shm/ctl/gen.tmp /dev/shm/ctl/gen
while :; do ok=0; for i in 0 1 2; do a=; read a b </dev/shm/ctl/done.$i 2>/dev/null; [ "$a" = "$g" ] && ok=$((ok+1)); done; [ $ok = 3 ] && break; sleep 0.02; done
cat /dev/shm/ctl/done.0 /dev/shm/ctl/done.1 /dev/shm/ctl/done.2`

const lazyDmesgPattern = `'BUG|Oops|stall|lockup|hung task|Bad page|corrupt|segfault|I/O error|Out of memory|Call Trace|soft lockup'`

const lazyDmesg = `dmesg | grep -ciE ` + lazyDmesgPattern + `; true`

type lazyArm struct {
	name          string
	private, keep bool
	mode, hv      string
	thpOff        bool
}

var lazyArms = []lazyArm{
	{name: "copy"},
	{name: "cow", private: true, keep: true, mode: lazyCOW, hv: lazyV55},
	{name: "cowthp", private: true, keep: true, mode: lazyCOW, hv: lazyV55, thpOff: true},
}

type lazyHost struct {
	*checkoutHost
	t     *testing.T
	base  string
	mem   int64
	mu    sync.Mutex
	marks map[string]time.Time
}

type lazyVM struct {
	h      *lazyHost
	arm    lazyArm
	key    string
	gen    int
	sum    string
	bootID string
	pids   string
	status string
	dmesg  string
	// held is the unit's anonymous and shared memory before the last save.
	held uint64
	ok   int
	bad  int
}

func lazyEnv(name, def string) string { return cmp.Or(os.Getenv(name), def) }

func (h *lazyHost) logf(format string, args ...any) { h.t.Logf("LAZY "+format, args...) }

func (v *lazyVM) sh(script string, args ...string) (string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	code, out, errOut, err := v.h.e.exec(ctx, v.key, append([]string{"/bin/sh", "-c", script, "sh"}, args...), "/workspace")
	took := time.Since(start)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s%s", code, out, errOut)
	}
	return strings.TrimSpace(out), took, err
}

func (v *lazyVM) root(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hp, err := v.h.e.helper(v.key)
	if err != nil {
		return "", err
	}
	rec, err := v.h.e.load(v.key)
	if err != nil {
		return "", err
	}
	_, out, err := v.h.e.runAsRoot(ctx, hp, rec, []string{"/bin/sh", "-c", script})
	return strings.TrimSpace(out), err
}

func (v *lazyVM) cgroup() string {
	return filepath.Join(v.h.e.cgroupRoot, runtime.SessionSlice, unitName(v.key))
}

// cg is the unit's memory as the kernel accounts it, and as the
// scheduler's MemoryUse reports it.
type cg struct {
	current, peak, limit, use uint64
	stat                      map[string]uint64
	events                    map[string]uint64
	read                      uint64
}

func lazyFields(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if k, val, ok := strings.Cut(line, " "); ok {
			out[k], _ = strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		}
	}
	return out
}

func (v *lazyVM) sample() cg {
	dir := v.cgroup()
	num := func(name string) uint64 {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		return n
	}
	c := cg{current: num("memory.current"), peak: num("memory.peak"), limit: num("memory.max")}
	data, _ := os.ReadFile(filepath.Join(dir, "memory.stat"))
	c.stat = lazyFields(data)
	data, _ = os.ReadFile(filepath.Join(dir, "memory.events"))
	c.events = lazyFields(data)
	use, _ := v.h.e.memoryUse()
	c.use = use[runtime.ID(idPrefix+v.key)]
	procs, _ := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	for pid := range strings.FieldsSeq(string(procs)) {
		io, _ := os.ReadFile("/proc/" + pid + "/io")
		c.read += lazyFields(bytes.ReplaceAll(io, []byte(":"), nil))["read_bytes"]
	}
	return c
}

func mib(n uint64) string { return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) }

func (c cg) String() string {
	s := c.stat
	return fmt.Sprintf("current=%s anon=%s file=%s active_file=%s inactive_file=%s shmem=%s file_mapped=%s anon_thp=%s memoryuse=%s truth_anon_shmem=%s gap=%s peak=%s max=%s ev_max=%d ev_oom_kill=%d pgmajfault=%d refault_file=%d read_mib=%s",
		mib(c.current), mib(s["anon"]), mib(s["file"]), mib(s["active_file"]), mib(s["inactive_file"]), mib(s["shmem"]), mib(s["file_mapped"]), mib(s["anon_thp"]),
		mib(c.use), mib(s["anon"]+s["shmem"]), strconv.FormatFloat((float64(c.use)-float64(s["anon"]+s["shmem"]))/(1<<20), 'f', 1, 64),
		mib(c.peak), mib(c.limit), c.events["max"], c.events["oom_kill"], s["pgmajfault"], s["workingset_refault_file"], mib(c.read))
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}

func newLazyHost(t *testing.T) *lazyHost {
	c := newCheckoutHost(t)
	mem, err := strconv.ParseInt(lazyEnv("ZZ_MEM", "2"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	h := &lazyHost{checkoutHost: c, t: t, mem: mem, marks: map[string]time.Time{}}
	zzMark = func(name string) {
		h.mu.Lock()
		h.marks[name] = time.Now()
		h.mu.Unlock()
	}
	t.Cleanup(func() {
		zzMark, zzPaused = func(string) {}, func(string) {}
		zzPrivate, zzKeep, zzRestoreMode, zzHypervisor = false, false, "", ""
	})
	h.base = c.workspace(t, "ws-lazy", 2000)
	for _, script := range []string{
		"grep -m1 'model name' /proc/cpuinfo; nproc; grep MemTotal /proc/meminfo; uname -r",
		"cloud-hypervisor --version; " + lazyV55 + " --version; sha256sum " + lazyV55,
		"stat -f -c 'fs=%T' " + c.e.root + "; df -h " + c.e.root + " | tail -1",
		"cat /sys/kernel/mm/transparent_hugepage/enabled /sys/kernel/mm/transparent_hugepage/shmem_enabled /sys/kernel/mm/transparent_hugepage/khugepaged/defrag",
	} {
		out, _ := exec.Command("sh", "-c", script).CombinedOutput()
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			h.logf("info %s", line)
		}
	}
	return h
}

// written is how many bytes the data directory's block device has been
// asked to write.
func (h *lazyHost) written() uint64 {
	src, _ := exec.Command("findmnt", "-no", "SOURCE", "-T", h.e.root).Output()
	data, _ := os.ReadFile("/sys/block/" + filepath.Base(strings.TrimSpace(string(src))) + "/stat")
	if f := strings.Fields(string(data)); len(f) > 6 {
		n, _ := strconv.ParseUint(f[6], 10, 64)
		return n * 512
	}
	return 0
}

func (h *lazyHost) set(arm lazyArm) {
	zzPrivate, zzKeep, zzRestoreMode, zzHypervisor = arm.private, arm.keep, arm.mode, arm.hv
}

// up boots a session VM under arm and loads it: 600 MiB in /dev/shm and
// the process tree.
func (h *lazyHost) up(arm lazyArm, suffix string) (*lazyVM, error) {
	ctx := context.Background()
	h.set(arm)
	v := &lazyVM{h: h, arm: arm, key: fmt.Sprintf("run-zz-%s-%d%s", arm.name, h.mem, suffix)}
	h.t.Cleanup(v.down)
	checkout, _, err := h.git.CreateRunCheckoutAt(ctx, "ws-lazy", domain.RunID(v.key), h.base, "main", "a task", "")
	if err != nil {
		return nil, err
	}
	spec := runtime.Spec{
		Image: cmp.Or(os.Getenv("AETHER_TEST_IMAGE"), checkoutImage), CreationKey: v.key, TTY: true, User: "1000:1000",
		Command: []string{"/bin/sh", "-c", "while [ ! -s /tmp/tree.py ]; do sleep 0.2; done; exec python3 /tmp/tree.py"},
		Env:     map[string]string{"AETHER_RUN_ID": v.key, "HOME": "/home/aether"}, CPULimit: 1, MemoryLimitBytes: h.mem << 30,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
	}
	if _, err = h.e.create(ctx, v.key, spec, options{}); err != nil {
		return nil, err
	}
	start := time.Now()
	if err = h.e.start(ctx, v.key); err != nil {
		return nil, err
	}
	booted := time.Since(start)
	if _, _, err = v.sh(`printf '%s' "$1" >/tmp/tree.py.tmp && mv /tmp/tree.py.tmp /tmp/tree.py`, lazyTree); err != nil {
		return nil, err
	}
	if v.sum, _, err = v.sh("head -c 629145600 /dev/urandom >/dev/shm/fill && sha256sum </dev/shm/fill"); err != nil {
		return nil, err
	}
	if _, _, err = v.sh("for i in $(seq 600); do [ -s /dev/shm/ctl/pids ] && [ -s /dev/shm/ctl/done.2 ] && exit 0; sleep 0.1; done; exit 1"); err != nil {
		return nil, fmt.Errorf("the tree did not start: %w", err)
	}
	if err = v.bump(); err != nil {
		return nil, err
	}
	v.bootID, _, _ = v.sh("cat /proc/sys/kernel/random/boot_id")
	v.pids, _, _ = v.sh("cat /dev/shm/ctl/pids")
	v.status, _, _ = v.sh("git status --porcelain | wc -l")
	v.dmesg, _ = v.root(lazyDmesg)
	var memArg string
	if l, launchErr := readLaunch(h.e.dir(v.key)); launchErr == nil {
		if i := slices.Index(l.Args, "--memory"); i >= 0 && i+1 < len(l.Args) {
			memArg = l.Args[i+1] + " hypervisor=" + l.Hypervisor
		}
	}
	h.logf("boot arm=%s mem=%d key=%s start_ms=%s memory_arg=%q boot_id=%s pids=%q dmesg_hits=%s git_status_lines=%s %s",
		arm.name, h.mem, v.key, ms(booted), memArg, v.bootID, v.pids, v.dmesg, v.status, v.sample())
	return v, nil
}

func (v *lazyVM) down() {
	ctx := context.Background()
	_ = v.h.e.destroy(ctx, v.key)
	_ = v.h.git.RemoveRunHistory(ctx, domain.RunID(v.key))
	v.h.e.promotions.Wait()
}

// bump moves the tree to the next generation, which rewrites a word of
// every page of its 300 MiB after checking the whole page.
func (v *lazyVM) bump() error { return v.bumpAs("") }

// bumpAs with a mode stamps the next generation without reading the pages
// first, so each page takes one write fault instead of a read fault and a
// copy-on-write fault.
func (v *lazyVM) bumpAs(mode string) error {
	v.gen++
	out, _, err := v.sh(lazyBump, strconv.Itoa(v.gen), mode)
	if err != nil {
		return err
	}
	if want := strings.Repeat(fmt.Sprintf("%d 0\n", v.gen), lazyKids); out+"\n" != want {
		return fmt.Errorf("the tree's memory is wrong at generation %d: %q", v.gen, out)
	}
	return nil
}

func (v *lazyVM) snapshotFile() string {
	return filepath.Join(v.h.e.path(v.key, snapshotDir), "memory-ranges")
}

func lazyThpOff(dir string) error {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&cfg); err != nil {
		return err
	}
	cfg["memory"].(map[string]any)["thp"] = false
	if data, err = json.Marshal(cfg); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

type lazyRound struct {
	kind string
	i    int
	cold bool
	// idle is how long the woken guest is left alone after its first
	// command, sampled at 5, 60 and 300 s.
	idle time.Duration
	// between runs after the save, before the restore.
	between func() error
	// blind makes the write tail stamp the heap without reading it first.
	blind bool
}

// round saves the VM, wakes it and checks it. It reports false when the
// guest is no longer usable.
func (v *lazyVM) round(r lazyRound) bool {
	h, ctx := v.h, context.Background()
	h.set(v.arm)
	tag := fmt.Sprintf("arm=%s mem=%d kind=%s i=%d", v.arm.name, h.mem, r.kind, r.i)
	fail := func(step string, err error) bool {
		if err != nil {
			v.bad++
			h.logf("round %s ok=false step=%q err=%q", tag, step, err)
		}
		return err != nil
	}
	pre := v.sample()
	v.held = pre.stat["anon"] + pre.stat["shmem"]
	wrote := h.written()
	start := time.Now()
	if err := h.e.save(ctx, v.key); err != nil {
		events, _ := os.ReadFile(filepath.Join(v.cgroup(), "memory.events"))
		kernel, _ := exec.Command("sh", "-c", "dmesg | tail -25").CombinedOutput()
		h.logf("savefail %s before_save: %s", tag, pre)
		h.logf("savefail %s memory.events=%q monitor_log=%q", tag, strings.Join(strings.Fields(string(events)), " "), h.e.logTail(v.key))
		for line := range strings.SplitSeq(strings.TrimSpace(string(kernel)), "\n") {
			h.logf("savefail %s host_dmesg %s", tag, line)
		}
		fail("save", err)
		return false
	}
	saved := time.Since(start)
	_ = exec.Command("sync").Run()
	wrote = h.written() - wrote
	var st unix.Stat_t
	_ = unix.Stat(v.snapshotFile(), &st)
	if v.arm.thpOff && fail("thp off", lazyThpOff(h.e.path(v.key, snapshotDir))) {
		return false
	}
	if r.between != nil && fail("between", r.between()) {
		return false
	}
	if r.cold {
		_ = exec.Command("sync").Run()
		if fail("drop caches", os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200)) {
			return false
		}
	}
	h.mu.Lock()
	clear(h.marks)
	h.mu.Unlock()
	t0 := time.Now()
	err := h.e.restore(ctx, v.key)
	restored := time.Since(t0)
	h.mu.Lock()
	m := map[string]time.Time{}
	for k, at := range h.marks {
		m[k] = at
	}
	h.mu.Unlock()
	if err != nil {
		v.bad++
		h.logf("round %s ok=false step=restore restore_ms=%s err=%q", tag, ms(restored), err)
		up, _ := h.e.running(ctx, v.key)
		return up
	}
	_, _, err = v.sh("true")
	first := time.Since(t0)
	if fail("first command", err) {
		return false
	}
	atWake := v.sample()
	for _, at := range []time.Duration{5 * time.Second, 60 * time.Second, 300 * time.Second} {
		if at > r.idle {
			break
		}
		time.Sleep(time.Until(t0.Add(first + at)))
		s := v.sample()
		h.logf("idle %s at_s=%d held_before_save=%s saved_alloc=%s anon_shmem_pct_of_held=%.1f %s", tag, int(at.Seconds()), mib(v.held), mib(uint64(st.Blocks)*512),
			100*float64(s.stat["anon"]+s.stat["shmem"])/float64(v.held), s)
	}
	t1 := time.Now()
	out, _, wsErr := v.sh("git status --porcelain | wc -l && sha256sum </dev/shm/fill")
	ws := time.Since(t1)
	atWS := v.sample()
	t2 := time.Now()
	var tailErr error
	if r.blind {
		tailErr = v.bumpAs("blind")
	} else {
		tailErr = v.bump()
	}
	tail := time.Since(t2)
	var wrong []string
	if wsErr != nil {
		wrong = append(wrong, "working set: "+wsErr.Error())
	} else if out != v.status+"\n"+v.sum {
		wrong = append(wrong, fmt.Sprintf("git status lines and sha256 %q, want %q", out, v.status+"\n"+v.sum))
	}
	if tailErr != nil {
		wrong = append(wrong, tailErr.Error())
	}
	state, _, _ := v.sh("cat /proc/sys/kernel/random/boot_id /dev/shm/ctl/pids; a=$(cat /dev/shm/ctl/hb); sleep 1; b=$(cat /dev/shm/ctl/hb); [ $b -gt $a ] && echo beating || echo stopped; " +
		"for p in $(cat /dev/shm/ctl/pids); do cut -d' ' -f22 /proc/${p%:*}/stat; done | tr '\\n' ' '")
	want := v.bootID + "\n" + v.pids + "\nbeating\n"
	var starts []string
	for p := range strings.FieldsSeq(v.pids) {
		_, s, _ := strings.Cut(p, ":")
		starts = append(starts, s)
	}
	want += strings.Join(starts, " ")
	if state != want {
		wrong = append(wrong, fmt.Sprintf("boot id, pids, heartbeat, start times %q, want %q", state, want))
	}
	if hits, hitErr := v.root(lazyDmesg); hitErr != nil || hits != v.dmesg {
		lines, _ := v.root("dmesg | grep -iE " + lazyDmesgPattern + " | tail -6")
		wrong = append(wrong, fmt.Sprintf("dmesg hits %q (%v), want %q: %s", hits, hitErr, v.dmesg, lines))
	}
	ok := len(wrong) == 0
	if ok {
		v.ok++
	} else {
		v.bad++
	}
	since := func(a, b string) string {
		if m[a].IsZero() || m[b].IsZero() {
			return "-"
		}
		return ms(m[b].Sub(m[a]))
	}
	h.logf("round %s ok=%t save_ms=%s save_wrote_mib=%s saved_alloc_mib=%s saved_len_mib=%s held_before_save_mib=%s exec_to_paused_ms=%s unit_ms=%s paused_to_resumed_ms=%s resume_to_helper_ms=%s restore_ms=%s first_cmd_ms=%s working_set_cmd_ms=%s to_working_set_ms=%s tail_cmd_ms=%s to_tail_ms=%s wrong=%q",
		tag, ok, ms(saved), mib(wrote), mib(uint64(st.Blocks)*512), mib(uint64(st.Size)), mib(v.held),
		ms(m["paused"].Sub(t0)), ms(m["unit"].Sub(t0)), since("paused", "resume"), since("resume", "helper"), ms(restored), ms(first),
		ms(ws), ms(first+ws), ms(tail), ms(first+ws+tail), strings.Join(wrong, "; "))
	h.logf("mem %s at=wake %s", tag, atWake)
	h.logf("mem %s at=working_set %s", tag, atWS)
	h.logf("mem %s at=tail %s", tag, v.sample())
	return true
}

// compare snapshots the restored, still paused VM again and compares the
// two memory files byte for byte.
func (v *lazyVM) compare(key string) {
	h := v.h
	dir := h.e.path(key, "zzcmp")
	_ = os.RemoveAll(dir)
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(dir, 0o700); err != nil {
		h.logf("pagecompare arm=%s mem=%d err=%q", v.arm.name, h.mem, err)
		return
	}
	start := time.Now()
	if err := newCHAPI(h.e.path(key, apiSocket)).snapshot(context.Background(), dir); err != nil {
		h.logf("pagecompare arm=%s mem=%d err=%q", v.arm.name, h.mem, err)
		return
	}
	snap := time.Since(start)
	second := filepath.Join(dir, "memory-ranges")
	var a, b unix.Stat_t
	_ = unix.Stat(v.snapshotFile(), &a)
	_ = unix.Stat(second, &b)
	pages, differ, where, err := lazyDiff(v.snapshotFile(), second)
	h.logf("pagecompare arm=%s mem=%d pages=%d pages_that_differ=%d first_offsets=%v same_size=%t err=%v first_alloc_mib=%s second_alloc_mib=%s second_snapshot_ms=%s",
		v.arm.name, h.mem, pages, differ, where, a.Size == b.Size, err, mib(uint64(a.Blocks)*512), mib(uint64(b.Blocks)*512), ms(snap))
}

// lazyDiff counts the 4 KiB pages in which two files differ.
func lazyDiff(first, second string) (pages, differ int, where []int64, err error) {
	a, err := os.Open(first)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = a.Close() }()
	b, err := os.Open(second)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = b.Close() }()
	x, y := make([]byte, 4<<20), make([]byte, 4<<20)
	for off := int64(0); ; off += int64(len(x)) {
		n, errA := a.ReadAt(x, off)
		m, _ := b.ReadAt(y, off)
		if n != m {
			return pages, differ, where, fmt.Errorf("lengths differ at %d", off)
		}
		for p := 0; p < n; p += 4096 {
			pages++
			if !bytes.Equal(x[p:min(p+4096, n)], y[p:min(p+4096, n)]) {
				differ++
				if len(where) < 8 {
					where = append(where, off+int64(p))
				}
			}
		}
		if errA != nil {
			return pages, differ, where, nil
		}
	}
}

// h8 has the guest take 512 MiB, free it, and take it again after the
// host dropped its caches.
func (v *lazyVM) h8() {
	h := v.h
	tag := fmt.Sprintf("arm=%s mem=%d", v.arm.name, h.mem)
	s0 := v.sample()
	h.logf("h8 %s step=before %s", tag, s0)
	_, w1, err := v.sh("head -c 536870912 /dev/urandom >/dev/shm/h8")
	if err != nil {
		h.logf("h8 %s err=%q", tag, err)
		return
	}
	s1 := v.sample()
	h.logf("h8 %s step=wrote_512 write_ms=%s %s", tag, ms(w1), s1)
	_, _, _ = v.sh("rm /dev/shm/h8")
	freed := time.Now()
	var s2 cg
	for time.Since(freed) < 90*time.Second {
		time.Sleep(5 * time.Second)
		s2 = v.sample()
		h.logf("h8 %s step=freed after_s=%d %s", tag, int(time.Since(freed).Seconds()), s2)
	}
	_ = exec.Command("sync").Run()
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200)
	s3 := v.sample()
	_, w2, err := v.sh("head -c 536870912 /dev/urandom >/dev/shm/h8")
	s4 := v.sample()
	h.logf("h8 %s step=wrote_again write_ms=%s err=%v %s", tag, ms(w2), err, s4)
	_, _, _ = v.sh("rm /dev/shm/h8")
	h.logf("h8 %s summary took_mib=%s returned_mib=%s first_write_ms=%s second_write_ms=%s reread_mib=%s majfaults=%d refaults=%d",
		tag, mib(s1.stat["anon"]+s1.stat["shmem"]-s0.stat["anon"]-s0.stat["shmem"]),
		strconv.FormatFloat((float64(s1.stat["anon"]+s1.stat["shmem"])-float64(s2.stat["anon"]+s2.stat["shmem"]))/(1<<20), 'f', 1, 64),
		ms(w1), ms(w2), mib(s4.read-s3.read), s4.stat["pgmajfault"]-s3.stat["pgmajfault"], s4.stat["workingset_refault_file"]-s3.stat["workingset_refault_file"])
}

// capTest reads everything the snapshot holds of the session and then
// rewrites it and 600 MiB more, so the snapshot's page cache and the
// guest's own pages together pass the unit's MemoryMax.
func (v *lazyVM) capTest() {
	h := v.h
	tag := fmt.Sprintf("arm=%s mem=%d", v.arm.name, h.mem)
	h.logf("cap %s step=before %s", tag, v.sample())
	_, _, err := v.sh("sha256sum </dev/shm/fill >/dev/null")
	h.logf("cap %s step=read_600 err=%v %s", tag, err, v.sample())
	_, _, err = v.sh("dd if=/dev/urandom of=/dev/shm/fill bs=1M count=600 conv=notrunc status=none")
	h.logf("cap %s step=rewrote_600 err=%v %s", tag, err, v.sample())
	err = v.bump()
	h.logf("cap %s step=rewrote_heap err=%v %s", tag, err, v.sample())
	_, _, err = v.sh("head -c 629145600 /dev/urandom >/dev/shm/extra")
	h.logf("cap %s step=wrote_600_more err=%v %s", tag, err, v.sample())
	state, _, err := v.sh("cat /proc/sys/kernel/random/boot_id")
	hits, _ := v.root(lazyDmesg)
	h.logf("cap %s step=after alive=%t dmesg_hits=%s want_hits=%s err=%v", tag, state == v.bootID, hits, v.dmesg, err)
	_, _, _ = v.sh("rm /dev/shm/extra")
	v.sum, _, _ = v.sh("sha256sum </dev/shm/fill")
}

func TestKVMZZLazyWake(t *testing.T) {
	h := newLazyHost(t)
	n, _ := strconv.Atoi(lazyEnv("ZZ_N", "10"))
	extra := lazyEnv("ZZ_EXTRA", "1") == "1"
	var totals []string
	for _, arm := range lazyArms {
		if !slices.Contains(strings.Split(lazyEnv("ZZ_ARMS", "copy,cow,cowthp"), ","), arm.name) {
			continue
		}
		var v *lazyVM
		alive := false
		// A VM whose first save fails is replaced, so the arm still gets
		// its wakes; each failure is logged and counted.
		for attempt := 0; attempt < 4 && !alive; attempt++ {
			var err error
			if v, err = h.up(arm, strings.Repeat("x", attempt)); err != nil {
				h.logf("boot arm=%s mem=%d err=%q", arm.name, h.mem, err)
				totals = append(totals, fmt.Sprintf("total arm=%s mem=%d attempt=%d boot failed", arm.name, h.mem, attempt))
				continue
			}
			if alive = v.round(lazyRound{kind: "first", cold: true}); !alive {
				totals = append(totals, fmt.Sprintf("total arm=%s mem=%d attempt=%d died in its first save and wake", arm.name, h.mem, attempt))
				v.down()
			}
		}
		if !alive {
			continue
		}
		first := fmt.Sprintf("first_ok=%d first_bad=%d", v.ok, v.bad)
		v.ok, v.bad = 0, 0
		for _, kind := range []string{"cold", "warm"} {
			for i := 0; alive && i < n; i++ {
				alive = v.round(lazyRound{kind: kind, i: i, cold: kind == "cold"})
			}
		}
		timed := fmt.Sprintf("%s timed_ok=%d timed_bad=%d", first, v.ok, v.bad)
		if alive && extra {
			alive = v.round(lazyRound{kind: "idle-cold", cold: true, idle: 60 * time.Second})
		}
		if alive && extra {
			alive = v.round(lazyRound{kind: "idle-warm", idle: 300 * time.Second})
		}
		if alive && extra {
			alive = v.round(lazyRound{kind: "blind-cold", cold: true, blind: true})
		}
		if alive && extra {
			alive = v.round(lazyRound{kind: "blind-warm", blind: true})
		}
		if alive && extra {
			zzPaused = func(key string) { v.compare(key) }
			alive = v.round(lazyRound{kind: "pagecompare", cold: true})
			zzPaused = func(string) {}
		}
		if alive && extra {
			v.h8()
			if h.mem <= 2 {
				v.capTest()
			}
			alive = v.round(lazyRound{kind: "after-extras", cold: true})
		}
		totals = append(totals, fmt.Sprintf("total arm=%s mem=%d %s all_ok=%d all_bad=%d alive=%t", arm.name, h.mem, timed, v.ok, v.bad, alive))
		v.down()
	}
	if extra && slices.Contains(strings.Split(lazyEnv("ZZ_ARMS", "copy,cow,cowthp"), ","), "copy") {
		totals = append(totals, lazyBaselines(h)...)
	}
	for _, l := range totals {
		h.logf("%s", l)
	}
}

// lazyBaselines are the rounds the checker has to fail or survive: a saved
// memory file with two pages zeroed behind the guest's back, one of the
// /dev/shm file and one of the tree's heap, and v54.1's on-demand restore
// as it was shipped in #1049.
func lazyBaselines(h *lazyHost) []string {
	var totals []string
	if v, err := h.up(lazyArm{name: "zeroed"}, ""); err != nil {
		h.logf("boot arm=zeroed err=%q", err)
	} else {
		page, _, _ := v.sh("dd if=/dev/shm/fill bs=4096 skip=70000 count=1 status=none | head -c 32 | od -An -tx1 | tr -d ' \\n'")
		gen := v.gen
		v.round(lazyRound{kind: "inject", cold: true, between: func() error {
			var shm []byte
			if _, err := fmt.Sscanf(page, "%x", &shm); err != nil {
				return err
			}
			heap := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, lazyMagic+1), uint32(gen)), 12345)
			return lazyPunch(h, v.snapshotFile(), shm, heap)
		}})
		totals = append(totals, fmt.Sprintf("total arm=zeroed mem=%d ok=%d bad=%d (want ok=0 bad=1)", h.mem, v.ok, v.bad))
		v.down()
	}
	arm := lazyArm{name: "ondemand54", mode: ",memory_restore_mode=ondemand"}
	if v, err := h.up(arm, ""); err != nil {
		h.logf("boot arm=ondemand54 err=%q", err)
	} else {
		alive := true
		for i := 0; alive && i < 3; i++ {
			alive = v.round(lazyRound{kind: "cold", i: i, cold: true})
			// Cloud Hypervisor refuses a snapshot until the background
			// load has ended.
			time.Sleep(20 * time.Second)
		}
		totals = append(totals, fmt.Sprintf("total arm=ondemand54 mem=%d ok=%d bad=%d alive=%t", h.mem, v.ok, v.bad, alive))
		v.down()
	}
	return totals
}

// lazyPunch zeroes each page of the memory file that starts with one of
// the prefixes.
func lazyPunch(h *lazyHost, path string, prefixes ...[]byte) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	found := make([]int, len(prefixes))
	buf := make([]byte, 4<<20)
	for off := int64(0); ; off += int64(len(buf)) {
		n, readErr := f.ReadAt(buf, off)
		for i, prefix := range prefixes {
			for p := 0; ; {
				at := bytes.Index(buf[p:n], prefix)
				if at < 0 {
					break
				}
				found[i]++
				page := (off + int64(p+at)) &^ 4095
				if err = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, page, 4096); err != nil {
					return err
				}
				p += at + len(prefix)
			}
		}
		if readErr != nil {
			break
		}
	}
	h.logf("inject zeroed pages of the saved memory file: shm_page_hits=%d heap_page_hits=%d", found[0], found[1])
	if found[0] == 0 || found[1] == 0 {
		return fmt.Errorf("pages to zero not found in %s: %v", path, found)
	}
	return f.Sync()
}
