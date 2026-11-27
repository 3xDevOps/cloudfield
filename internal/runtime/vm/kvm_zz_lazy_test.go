//go:build kvm

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// Scratch code for the lazy-restore experiment, never merged. It saves and
// wakes one session VM many times in one arm and prints "LAZY ..." lines.

type lazyArm struct {
	v55     bool
	bootMem string
	mode    string
	inject  bool
	// rewrite makes the saved configuration's memory private before a
	// restore, or digs holes in a memory file a private VM wrote.
	rewrite bool
}

var lazyArms = map[string]lazyArm{
	"A54": {false, "shared=on", "ondemand", true, false},
	"A55": {true, "shared=on", "ondemand", true, false},
	"B54": {false, "shared=on", "ondemand", false, false},
	"B55": {true, "shared=on", "ondemand", false, false},
	"C":   {true, "shared=off", "copyonwrite", true, false},
	"D":   {true, "shared=on", "copyonwrite", true, true},
	"E":   {true, "shared=on", "", true, false},
	"E54": {false, "shared=on", "", true, false},
}

const (
	lazyKey   = "run-lazy"
	lazyGuest = "/usr/local/bin/lazyguest -test.run '^TestKVMZZLazyGuestNow$' -test.timeout 0"
	lazyDmesg = "Kernel panic|BUG|Oops|Bad page|bad pte|general protection|Call Trace|rcu.*stall|soft lockup|segfault|Out of memory|oom-kill"
)

type lazyRun struct {
	t      *testing.T
	e      *engine
	name   string
	arm    lazyArm
	mib    int
	bin    string
	cgroup string
	began  time.Time

	alive, shm string
	workIter   int
	explained  map[uint64][2]int
	dFallback  bool

	cycles, failed, rewriteRejected int
	wake                            map[string][]float64
	restored                        map[string][]float64
	totals                          map[string]int
}

func (r *lazyRun) say(format string, args ...any) {
	fmt.Printf("LAZY arm=%s mib=%d t=%ds %s\n", r.name, r.mib, int(time.Since(r.began).Seconds()), fmt.Sprintf(format, args...))
}

func (r *lazyRun) guest(script string, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	code, out, errOut, err := r.e.exec(ctx, lazyKey, []string{"/bin/sh", "-c", script}, "")
	return out + errOut, code, err
}

func lazyField(out, name string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			return strings.TrimSpace(rest)
		}
		if line == name {
			return ""
		}
	}
	return "<missing>"
}

func (r *lazyRun) logs(why string) {
	for _, name := range []string{monitorLog, consoleLog} {
		data, _ := readNoFollow(r.e.path(lazyKey, name))
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for _, line := range lines[max(0, len(lines)-40):] {
			r.say("LOG why=%q file=%s | %s", why, name, line)
		}
	}
}

// monitorNotes are the lines of the running monitor's log that say how it
// restored or saved.
func (r *lazyRun) monitorNotes() (notes []string, fellBack bool) {
	data, _ := readNoFollow(r.e.path(lazyKey, monitorLog))
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, "falling back") {
			fellBack = true
		}
		if regexp.MustCompile(`falling back|WARN|ERRO|Restore \(mode|UFFD|restore source`).MatchString(line) && len(notes) < 12 {
			notes = append(notes, line)
		}
	}
	return notes, fellBack
}

// setup boots a fresh VM and starts the pattern holder, the 1 GiB file and
// the workload in it.
func (r *lazyRun) setup() error {
	ctx := context.Background()
	_ = r.e.destroy(ctx, lazyKey)
	scratchBin, scratchMemory = r.bin, r.arm.bootMem
	if r.dFallback {
		scratchMemory = "shared=off"
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	image := os.Getenv("AETHER_TEST_IMAGE")
	if image == "" {
		image = "docker.io/library/ubuntu:24.04"
	}
	spec := runtime.Spec{
		Image: image, CreationKey: lazyKey, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": lazyKey}, CPULimit: 2, MemoryLimitBytes: int64(r.mib) << 20,
		Mounts: []runtime.Mount{{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true}},
	}
	if _, err = r.e.create(ctx, lazyKey, spec, options{Offline: true}); err != nil {
		return err
	}
	start := time.Now()
	if err = r.e.start(ctx, lazyKey); err != nil {
		return err
	}
	booted := time.Since(start)
	// At 2 GiB the session's memory limit cannot hold 70% of RAM in the
	// pattern beside the file and the workload's 512 MiB.
	pattern, shm, work := r.mib*68/100, 1024, 512
	if r.mib < 4096 {
		pattern, shm = r.mib*39/100, 384
	}
	out, code, err := r.guest(fmt.Sprintf("set -e; cp /opt/aether-vmtest /usr/local/bin/lazyguest; chmod +x /usr/local/bin/lazyguest; "+
		"AETHER_LAZY_GUEST=pattern AETHER_LAZY_MIB=%d setsid %s </dev/null >/var/tmp/lazy-pattern.log 2>&1 & "+
		"head -c %dM /dev/urandom >/dev/shm/fill; echo SHM $(sha256sum </dev/shm/fill | cut -d' ' -f1); "+
		"AETHER_LAZY_GUEST=work AETHER_LAZY_MIB=%d setsid %s </dev/null >/var/tmp/lazy-work.log 2>&1 & "+
		"echo MEM $(awk '/^MemTotal:/ {print $2}' /proc/meminfo) $(cat /sys/fs/cgroup/session/memory.max 2>&1) $(cat /sys/kernel/mm/transparent_hugepage/enabled); "+
		"grep 'System RAM' /proc/iomem | sed 's/^/IOMEM /'; grep -E ' (wall_clock|hv_clock_boot|_text|__bss_start|__bss_stop)$' /proc/kallsyms | sed 's/^/KSYM /'",
		pattern, lazyGuest, shm, work, lazyGuest), 5*time.Minute)
	if err != nil || code != 0 {
		return fmt.Errorf("guest setup: exit %d, %v: %s", code, err, out)
	}
	r.shm = lazyField(out, "SHM")
	// The guest kernel's wall_clock is the 12-byte struct pvclock_wall_clock
	// it gave KVM by MSR_KVM_WALL_CLOCK_NEW; KVM writes it each time the
	// MSR is set, as a restore does. The kernel is loaded unrelocated, so
	// its physical address is the symbol's less __START_KERNEL_map.
	r.explained = map[uint64][2]int{}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "IOMEM ") || strings.HasPrefix(line, "KSYM ") || strings.HasPrefix(line, "MEM ") {
			r.say("SETUP %s", line)
		}
		if m := regexp.MustCompile(`^KSYM ([0-9a-f]{16}) \S wall_clock$`).FindStringSubmatch(line); m != nil {
			addr, _ := strconv.ParseUint(m[1], 16, 64)
			if gpa := addr - 0xffffffff80000000; addr > 0xffffffff80000000 && gpa < 3<<30 {
				r.explained[gpa&^4095] = [2]int{int(gpa & 4095), int(gpa&4095) + 12}
			}
		}
	}
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
		out, code, err = r.guest("AETHER_LAZY_GUEST=verify AETHER_LAZY_ASK=verify "+lazyGuest+"; cat /var/tmp/lazy-work.status", 5*time.Minute)
		if err == nil && strings.Contains(out, "VERIFY ") && !strings.Contains(out, "No such file") {
			break
		}
		if time.Now().After(deadline) {
			tail, _, _ := r.guest("tail -5 /var/tmp/lazy-pattern.log /var/tmp/lazy-work.log", time.Minute)
			return fmt.Errorf("the guest's pattern holder and workload did not start: exit %d, %v: %s; %s", code, err, out, tail)
		}
	}
	r.say("SETUP first %s", strings.ReplaceAll(strings.TrimSpace(out), "\n", " | "))
	if out, code, err = r.guest(lazyAliveScript, time.Minute); err != nil || code != 0 {
		return fmt.Errorf("guest identity: exit %d, %v: %s", code, err, out)
	}
	r.alive = lazyField(out, "ALIVE")
	r.workIter = 0
	pages := make([]string, 0, len(r.explained))
	for off, span := range r.explained {
		pages = append(pages, fmt.Sprintf("%#x[%#x,%#x)", off, span[0], span[1]))
	}
	r.say("SETUP booted_ms=%.0f pattern_mib=%d shm_mib=%d work_mib=%d bin=%q boot_memory=%q shm_sha256=%s alive=%q host_written_pages=%v",
		ms(booted), pattern, shm, work, r.bin, scratchMemory, r.shm, r.alive, pages)
	return nil
}

const lazyAliveScript = "echo ALIVE $(cat /proc/sys/kernel/random/boot_id) $(for p in pattern work; do pid=$(pgrep -f \"^/usr/local/bin/lazyguest\" | while read q; do " +
	"grep -qz \"AETHER_LAZY_GUEST=$p\" /proc/$q/environ && echo $q; done | head -1); echo $p:$pid:$(cut -d' ' -f22 /proc/$pid/stat); done) sleep:$(pgrep -x sleep | head -1)"

func lazyDropCaches() error {
	unix.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0)
}

func lazyBlocks(path string) (size, onDisk int64) {
	info, err := os.Stat(path)
	if err != nil {
		return -1, -1
	}
	return info.Size(), info.Sys().(*syscall.Stat_t).Blocks * 512
}

// privateConfig makes the memory of the saved VM configuration in dir
// private. It reports whether it was shared.
func lazyPrivateConfig(dir string) (bool, error) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var cfg map[string]any
	if err = dec.Decode(&cfg); err != nil {
		return false, err
	}
	memory, ok := cfg["memory"].(map[string]any)
	if !ok {
		return false, errors.New("config.json has no memory object")
	}
	if shared, _ := memory["shared"].(bool); !shared {
		return false, nil
	}
	memory["shared"] = false
	if data, err = json.Marshal(cfg); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, data, 0o600)
}

type lazyTimes struct{ unit, restored, resumed, helper time.Duration }

// restorePaused starts a monitor on the saved state and waits until it
// holds the VM, paused.
func (r *lazyRun) restorePaused(dir string, began time.Time) (lazyTimes, error) {
	var tm lazyTimes
	ctx := context.Background()
	rec, err := r.e.load(lazyKey)
	if err != nil {
		return tm, err
	}
	booted, err := readLaunch(r.e.dir(lazyKey))
	if err != nil {
		return tm, err
	}
	source := "source_url=file://" + dir
	if r.arm.mode != "" {
		source += ",memory_restore_mode=" + r.arm.mode
	}
	l := launch{Bin: r.bin, Home: booted.Home, Reveal: booted.Reveal, Args: []string{
		"-v", "--api-socket", "path=" + r.e.path(lazyKey, apiSocket), "--event-monitor", "path=" + r.e.path(lazyKey, eventsFile),
		"--restore", source,
	}}
	if err = r.e.launch(ctx, rec, l); err != nil {
		return tm, err
	}
	tm.unit = time.Since(began)
	loadCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err = r.e.awaitRestored(loadCtx, lazyKey, newCHAPI(r.e.path(lazyKey, apiSocket)))
	tm.restored = time.Since(began)
	return tm, err
}

func (r *lazyRun) prepareCache(cold bool, file string) error {
	if cold {
		return lazyDropCaches()
	}
	return exec.Command("cat", file).Run()
}

// mapped reports how the monitor maps guest RAM, and whether all of it is
// a private mapping of the memory file.
func (r *lazyRun) mapped(file string) (string, bool) {
	regions := lazyRAMMaps(lazyCHPid(r.cgroup))
	var private uint64
	for _, reg := range regions {
		if reg.perms == "rw-p" && strings.HasPrefix(reg.path, file) {
			private += reg.end - reg.start
		}
	}
	return lazyRegionsString(regions), private == uint64(guestMiB(int64(r.mib)))<<20
}

// cycle is one save and wake. It returns false when the VM must be made
// again.
func (r *lazyRun) cycle(n int, cold bool) bool {
	ctx := context.Background()
	e := r.e
	temp := "warm"
	if cold {
		temp = "cold"
	}
	dir := e.path(lazyKey, snapshotDir)
	file := filepath.Join(dir, "memory-ranges")
	api := newCHAPI(e.path(lazyKey, apiSocket))
	fail := func(what string, format string, args ...any) bool {
		r.failed++
		r.totals["fail_"+what]++
		r.say("CYCLE n=%d temp=%s ok=false fail=%s %s", n, temp, what, fmt.Sprintf(format, args...))
		r.logs(what)
		return false
	}

	start := time.Now()
	if err := e.save(ctx, lazyKey); err != nil {
		return fail("save", "%v", err)
	}
	saveTook := time.Since(start)
	size, onDisk := lazyBlocks(file)
	sparse := "as-saved"
	if r.arm.rewrite {
		was := onDisk
		start = time.Now()
		rewrote, err := lazyPrivateConfig(dir)
		if err != nil {
			return fail("rewrite", "%v", err)
		}
		sparse = "config-rewritten-to-private"
		if !rewrote {
			sparse = "dig-holes"
			if out, err := exec.Command("fallocate", "--dig-holes", file).CombinedOutput(); err != nil {
				return fail("dig", "%v: %s", err, out)
			}
			_, onDisk = lazyBlocks(file)
		}
		r.say("SPARSE n=%d how=%s disk_before_mib=%d disk_after_mib=%d took_ms=%.0f", n, sparse, was>>20, onDisk>>20, ms(time.Since(start)))
	}
	r.say("SAVE n=%d temp=%s save_ms=%.0f file_mib=%d disk_mib=%d sparse=%s", n, temp, ms(saveTook), size>>20, onDisk>>20, sparse)

	// The page compare: restore, leave the guest paused, save again.
	if err := r.prepareCache(cold, file); err != nil {
		return fail("cache", "%v", err)
	}
	var inj *lazyInjector
	began := time.Now()
	if r.arm.inject {
		inj = lazyInject(r.cgroup, began)
	}
	tm, err := r.restorePaused(dir, began)
	injLines, injOffs := inj.end()
	for _, line := range injLines {
		r.say("COMPARE-%s n=%d", line, n)
	}
	if err != nil {
		if sparse == "config-rewritten-to-private" {
			r.rewriteRejected++
			r.dFallback = true
			r.say("D-REWRITE-REJECTED n=%d: %v", n, err)
			r.logs("rewrite-rejected")
			e.kill(ctx, lazyKey)
			return false
		}
		e.kill(ctx, lazyKey)
		return fail("compare-restore", "%v", err)
	}
	maps, private := r.mapped(file)
	notes, fellBack := r.monitorNotes()
	resave := e.path(lazyKey, "lazy-resave")
	_ = os.RemoveAll(resave)
	if err = os.Mkdir(resave, 0o700); err != nil {
		return fail("resave", "%v", err)
	}
	start = time.Now()
	waited := 0
	for {
		err = api.snapshot(ctx, resave)
		if err == nil || !strings.Contains(err.Error(), "still in progress") || time.Since(start) > 20*time.Minute {
			break
		}
		waited++
		time.Sleep(250 * time.Millisecond)
	}
	resaveTook := time.Since(start)
	e.kill(ctx, lazyKey)
	if err != nil {
		_ = os.RemoveAll(resave)
		return fail("resave", "%v", err)
	}
	start = time.Now()
	cmp, err := lazyCompare(file, filepath.Join(resave, "memory-ranges"), r.explained)
	_, resaveDisk := lazyBlocks(filepath.Join(resave, "memory-ranges"))
	_ = os.RemoveAll(resave)
	if err != nil {
		return fail("compare", "%v", err)
	}
	injected := 0
	for i, d := range cmp.list {
		if injOffs[d.off] {
			injected++
		}
		if i < 6 || d.off == 0x20E6000 {
			r.say("DIFFPAGE n=%d off=%#x orig_zero=%v restored_zero=%v explained=%v injected=%v bytes=%d detail=[%s]", n, d.off, d.origZero, d.restZero, d.explained, injOffs[d.off], d.bytes, d.detail)
		}
	}
	offs := make([]string, 0, 48)
	for _, d := range cmp.list {
		if !d.explained && len(offs) < 48 {
			offs = append(offs, fmt.Sprintf("%#x", d.off))
		}
	}
	r.say("COMPARE n=%d temp=%s restored_ms=%.0f resave_ms=%.0f resave_waits=%d resave_disk_mib=%d compare_ms=%.0f pages=%d differ=%d unexplained=%d restored_zero=%d restored_zero_unexplained=%d orig_zero=%d differ_at_injected=%d injected_zero_reads=%d fell_back=%v private_map=%v maps=[%s] unexplained_offs=%v",
		n, temp, ms(tm.restored), ms(resaveTook), waited, resaveDisk>>20, ms(time.Since(start)), cmp.pages, cmp.differ, cmp.unexplained, cmp.restZero, cmp.restZeroUnexplained, cmp.origZero, injected, len(injOffs), fellBack, private, maps, offs)
	for _, note := range notes {
		r.say("COMPARE-MONITOR n=%d | %s", n, note)
	}
	r.totals["compare_differ_pages"] += cmp.differ
	r.totals["compare_unexplained_pages"] += cmp.unexplained
	r.totals["compare_restored_zero_unexplained_pages"] += cmp.restZeroUnexplained
	if cmp.unexplained > 0 {
		r.totals["compare_bad_cycles"]++
	}
	wantPrivate := r.arm.mode == "copyonwrite"
	if fellBack {
		r.totals["fell_back_restores"]++
	}
	if wantPrivate && !private {
		r.totals["not_private_restores"]++
	}

	// The wake that is timed and that the guest runs after.
	if err = r.prepareCache(cold, file); err != nil {
		return fail("cache", "%v", err)
	}
	inj = nil
	began = time.Now()
	if r.arm.inject {
		inj = lazyInject(r.cgroup, began)
	}
	tm, err = r.restorePaused(dir, began)
	injLines, _ = inj.end()
	for _, line := range injLines {
		r.say("WAKE-%s n=%d", line, n)
	}
	if err != nil {
		e.kill(ctx, lazyKey)
		return fail("wake-restore", "%v", err)
	}
	maps, private = r.mapped(file)
	if err = api.resume(ctx); err != nil {
		e.kill(ctx, lazyKey)
		return fail("resume", "%v", err)
	}
	tm.resumed = time.Since(began)
	h, err := e.helper(lazyKey)
	if err == nil {
		err = e.waitHelper(ctx, lazyKey, h)
	}
	tm.helper = time.Since(began)
	notes, fellBack2 := r.monitorNotes()
	r.say("WAKE n=%d temp=%s unit_ms=%.1f restored_ms=%.1f resumed_ms=%.1f helper_ms=%.1f helper_err=%v fell_back=%v private_map=%v maps=[%s]",
		n, temp, ms(tm.unit), ms(tm.restored), ms(tm.resumed), ms(tm.helper), err, fellBack2, private, maps)
	for _, note := range notes {
		r.say("WAKE-MONITOR n=%d | %s", n, note)
	}
	if fellBack2 {
		r.totals["fell_back_restores"]++
	}
	if wantPrivate && !private {
		r.totals["not_private_restores"]++
	}
	if err != nil {
		r.totals["guest_never_answered"]++
		fail("helper", "%v", err)
		e.kill(ctx, lazyKey)
		return false
	}
	r.wake[temp] = append(r.wake[temp], ms(tm.helper))
	r.restored[temp] = append(r.restored[temp], ms(tm.restored))
	if err = h.SyncClock(ctx, time.Now()); err != nil {
		return fail("clock", "%v", err)
	}
	rec, err := e.load(lazyKey)
	if err != nil {
		return fail("record", "%v", err)
	}
	rec.Saved, rec.Resumed, rec.Exited = false, nil, false
	if err = e.store(rec); err != nil {
		return fail("record", "%v", err)
	}

	start = time.Now()
	out, code, err := r.guest(lazyAliveScript+"; echo SHM $(sha256sum </dev/shm/fill | cut -d' ' -f1); "+
		"echo DMESG $(dmesg | grep -cE '"+lazyDmesg+"'); dmesg | grep -E '"+lazyDmesg+"' | head -6 | sed 's/^/DMESGLINE /'; "+
		"AETHER_LAZY_GUEST=verify AETHER_LAZY_ASK=verify "+lazyGuest+" | grep -E '^(VERIFY|LAZYGUEST)'; echo WORK $(cat /var/tmp/lazy-work.status)", 15*time.Minute)
	if err != nil {
		r.totals["guest_checks_unanswered"]++
		return fail("guest-exec", "exit %d, %v: %s", code, err, out)
	}
	verify := lazyField(out, "VERIFY")
	var problems []string
	if alive := lazyField(out, "ALIVE"); alive != r.alive {
		problems = append(problems, fmt.Sprintf("identity %q, want %q", alive, r.alive))
		r.totals["identity_changed"]++
	}
	if shm := lazyField(out, "SHM"); shm != r.shm {
		problems = append(problems, fmt.Sprintf("sha256 of /dev/shm/fill %q, want %q", shm, r.shm))
		r.totals["shm_sha256_changed"]++
	}
	if d := lazyField(out, "DMESG"); d != "0" {
		problems = append(problems, "dmesg matches: "+d)
		r.totals["dmesg_bad"]++
	}
	for _, kind := range []string{"zero", "stale", "other"} {
		m := regexp.MustCompile(kind + `=(\d+)`).FindStringSubmatch(verify)
		if m == nil {
			problems = append(problems, "no pattern "+kind+" count")
			continue
		}
		if count, _ := strconv.Atoi(m[1]); count > 0 {
			problems = append(problems, fmt.Sprintf("pattern %s pages %d", kind, count))
			r.totals["pattern_"+kind+"_pages"] += count
		}
	}
	workFields := strings.Fields(lazyField(out, "WORK"))
	iter, bad := 0, -1
	if len(workFields) == 2 {
		iter, _ = strconv.Atoi(workFields[0])
		bad, _ = strconv.Atoi(workFields[1])
	}
	// The workload must end a round after the wake.
	for deadline := time.Now().Add(time.Minute); iter <= r.workIter && time.Now().Before(deadline); time.Sleep(time.Second) {
		if again, _, againErr := r.guest("cat /var/tmp/lazy-work.status", time.Minute); againErr == nil {
			if f := strings.Fields(again); len(f) == 2 {
				iter, _ = strconv.Atoi(f[0])
				bad, _ = strconv.Atoi(f[1])
			}
		}
	}
	if iter <= r.workIter || bad != 0 {
		problems = append(problems, fmt.Sprintf("workload at round %d (was %d), %d trees read back changed", iter, r.workIter, bad))
		r.totals["workload_bad"]++
	}
	r.workIter = iter
	r.say("GUEST n=%d temp=%s checks_ms=%.0f work_round=%d %s", n, temp, ms(time.Since(start)), iter, strings.ReplaceAll(strings.TrimSpace(out), "\n", " | "))
	compareOK := cmp.unexplained == 0 && !(wantPrivate && (fellBack || fellBack2 || !private))
	if len(problems) > 0 {
		r.totals["guest_bad_cycles"]++
		return fail("guest", "compare_ok=%v problems=%q", compareOK, problems)
	}
	if !compareOK {
		r.failed++
		r.say("CYCLE n=%d temp=%s ok=false fail=compare differ=%d unexplained=%d fell_back=%v private_map=%v", n, temp, cmp.differ, cmp.unexplained, fellBack || fellBack2, private)
		return true
	}
	r.say("CYCLE n=%d temp=%s ok=true differ=%d unexplained=0 helper_ms=%.1f", n, temp, cmp.differ, ms(tm.helper))
	return true
}

func lazyQuantiles(v []float64) string {
	if len(v) == 0 {
		return "n=0"
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return fmt.Sprintf("n=%d min=%.0f median=%.0f p90=%.0f max=%.0f", len(s), s[0], s[len(s)/2], s[min(len(s)-1, len(s)*9/10)], s[len(s)-1])
}

func TestKVMZZLazy(t *testing.T) {
	name := os.Getenv("LAZY_ARM")
	arm, ok := lazyArms[name]
	if !ok {
		t.Skip("set LAZY_ARM to one of the experiment's arms")
	}
	env := func(key string, fallback int) int {
		if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
			return v
		}
		return fallback
	}
	e := kvmEngine(t)
	r := &lazyRun{
		t: t, e: e, name: name, arm: arm, mib: env("LAZY_MIB", 2048), began: time.Now(),
		cgroup: filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(lazyKey)),
		wake:   map[string][]float64{}, restored: map[string][]float64{}, totals: map[string]int{},
	}
	if arm.v55 {
		r.bin = os.Getenv("LAZY_CH55")
		if r.bin == "" {
			t.Fatal("set LAZY_CH55 to Cloud Hypervisor v55.0")
		}
	}
	t.Cleanup(func() { _ = e.destroy(context.Background(), lazyKey) })
	cold, warm := env("LAZY_COLD", 30), env("LAZY_WARM", 10)
	budget := time.Duration(env("LAZY_BUDGET_MIN", 300)) * time.Minute
	var statfs unix.Statfs_t
	_ = unix.Statfs(e.root, &statfs)
	shmem, _ := os.ReadFile("/sys/kernel/mm/transparent_hugepage/shmem_enabled")
	thp, _ := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled")
	r.say("ENV arm=%+v cold=%d warm=%d data=%s fs_magic=%#x free_gib=%d host_thp=%q host_shmem_thp=%q",
		arm, cold, warm, e.root, statfs.Type, statfs.Bavail*uint64(statfs.Bsize)>>30, strings.TrimSpace(string(thp)), strings.TrimSpace(string(shmem)))

	up := false
	setupFails := 0
	for n := 1; n <= cold+warm; n++ {
		if time.Since(r.began) > budget {
			r.say("BUDGET-STOP before cycle %d of %d", n, cold+warm)
			break
		}
		for !up {
			err := r.setup()
			if err == nil {
				up = true
				break
			}
			setupFails++
			r.say("SETUP-FAIL %d: %v", setupFails, err)
			r.logs("setup")
			if setupFails >= 4 {
				t.Fatalf("the VM could not be set up %d times", setupFails)
			}
		}
		r.cycles++
		up = r.cycle(n, n <= cold)
	}
	for _, temp := range []string{"cold", "warm"} {
		r.say("SUMMARY-WAKE temp=%s exec_to_helper_ms %s | exec_to_restored_ms %s", temp, lazyQuantiles(r.wake[temp]), lazyQuantiles(r.restored[temp]))
	}
	keys := make([]string, 0, len(r.totals))
	for k := range r.totals {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, r.totals[k])
	}
	r.say("SUMMARY cycles=%d failed=%d passed=%d rewrite_rejected=%d d_fallback=%v setup_fails=%d %s",
		r.cycles, r.failed, r.cycles-r.failed, r.rewriteRejected, r.dFallback, setupFails, strings.Join(parts, " "))
}
