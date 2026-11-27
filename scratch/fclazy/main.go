// Firecracker lazy-restore and diff-snapshot experiment. Scratch code, never
// merged. It prints one "FCLAZY <kind> k=v ..." line per measurement and
// "FCLAZY-SUM ..." lines with n, min, median and p90 (nearest rank).
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	fcBin    = flag.String("fc", "", "firecracker binary")
	editor   = flag.String("editor", "", "snapshot-editor binary")
	chBin    = flag.String("ch", "/usr/local/bin/cloud-hypervisor", "")
	kernel   = flag.String("kernel", "", "")
	rootfs   = flag.String("rootfs", "", "raw root disk; must be on the filesystem of -work")
	dataDisk = flag.String("data", "", "raw ext4 data disk to copy per VM directory")
	work     = flag.String("work", "", "")
	fsName   = flag.String("fs", "ext4", "label for the lines")
	mem      = flag.Int("mem", 2048, "guest MiB")
	cycles   = flag.Int("cycles", 12, "")
	mcCycles = flag.Int("mincore-cycles", 12, "")
	churnMin = flag.Int("churn-min", 10, "")
	n        = flag.Int("n", 10, "wake timing samples per state")
	tap      = flag.String("tap", "fctap0", "")
	merge    = flag.String("merge", "inplace", "inplace or reflink")
	sections = flag.String("sections", "chain,timing,idle,baseline,frag,mincore,chcow", "")
)

const heapMiB = 512

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }

var (
	stats = map[string][]float64{}
	order []string
)

func record(key string, d time.Duration) {
	if _, ok := stats[key]; !ok {
		order = append(order, key)
	}
	stats[key] = append(stats[key], float64(d.Microseconds())/1000)
}

func summarize() {
	for _, key := range order {
		v := append([]float64(nil), stats[key]...)
		sort.Float64s(v)
		rank := func(p float64) float64 {
			i := int(p*float64(len(v))+0.9999999) - 1
			return v[max(i, 0)]
		}
		fmt.Printf("FCLAZY-SUM %s n=%d min_ms=%.1f med_ms=%.1f p90_ms=%.1f max_ms=%.1f\n", key, len(v), v[0], rank(0.5), rank(0.9), v[len(v)-1])
	}
	stats, order = map[string][]float64{}, nil
}

func shmMiB() int {
	if *mem >= 4096 {
		return 1024
	}
	return 320
}

// stableMiB makes the three checked areas 70% of guest RAM together.
func stableMiB() int { return *mem*70/100 - heapMiB - shmMiB() }

// newDir makes a VM directory holding the root disk (a hard link) and its
// own data disk.
func newDir(name string) (string, error) {
	dir := filepath.Join(*work, name)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.Link(*rootfs, filepath.Join(dir, "rootfs.ext4")); err != nil {
		return "", err
	}
	_, err := copyFile(*dataDisk, filepath.Join(dir, "data.raw"))
	return dir, err
}

type verified struct {
	ok, zero, stale, bad int
	shaOK, bootOK        bool
	took                 time.Duration
}

func (v verified) pass() bool {
	return v.zero == 0 && v.stale == 0 && v.bad == 0 && v.shaOK && v.bootOK
}

func (v verified) String() string {
	return fmt.Sprintf("pattern_ok=%d pattern_zero=%d pattern_stale=%d pattern_bad=%d sha256_ok=%t boot_id_same=%t verify_ms=%s", v.ok, v.zero, v.stale, v.bad, v.shaOK, v.bootOK, ms(v.took))
}

// verify has the guest read every word of every pattern page against the
// generations the host expects, and re-hash its /dev/shm file.
func verify(m *vm, e *expect) (verified, error) {
	var v verified
	t := time.Now()
	for _, id := range []int{stableID, heapID} {
		g := e.gens[id]
		for start := 0; start < len(g); {
			end := start
			for end < len(g) && g[end] == g[start] {
				end++
			}
			out, err := m.guest("check %d %d %d %d", id, start, end-start, g[start])
			if err != nil {
				return v, err
			}
			v.ok += fieldInt(out, "ok")
			v.zero += fieldInt(out, "zero")
			v.stale += fieldInt(out, "stale")
			v.bad += fieldInt(out, "bad")
			start = end
		}
	}
	out, err := m.guest("shmsum")
	if err != nil {
		return v, err
	}
	v.shaOK = field(out, "sha256") == e.shmSHA
	if out, err = m.guest("info"); err != nil {
		return v, err
	}
	v.bootOK = field(out, "boot_id") == e.bootID
	v.took = time.Since(t)
	return v, nil
}

// workloadRuns reports whether the guest's alloc/free loop makes progress.
func workloadRuns(m *vm) bool {
	a, err := m.guest("info")
	if err != nil {
		return false
	}
	time.Sleep(500 * time.Millisecond)
	b, err := m.guest("info")
	return err == nil && fieldInt(b, "wl") > fieldInt(a, "wl")
}

type saved struct {
	dir         string
	cfg         vmConfig
	base, state string
	e           *expect
}

func printConsole(id, dir string) int {
	bad, lines := console(dir)
	for i, l := range lines {
		if i >= 40 {
			fmt.Printf("FCLAZY CONSOLE %s ... %d more lines\n", id, len(lines)-i)
			break
		}
		fmt.Printf("FCLAZY CONSOLE %s %s\n", id, l)
	}
	return bad
}

// churnLoop frees and reuses guest memory in random sizes for d, part of it
// as single pages pinned apart, and prints the monitor's mapping count.
func churnLoop(m *vm, id string, d time.Duration) error {
	rng := rand.New(rand.NewPCG(1, 2))
	room := *mem*30/100 - 200
	deadline, next := time.Now().Add(d), time.Now()
	peak := 0
	for i := 0; time.Now().Before(deadline); i++ {
		if _, err := m.guest("churn %d", 16+rng.IntN(room/2)); err != nil {
			return err
		}
		if i%4 == 3 {
			if _, err := m.guest("frag 3 %d", 16+rng.IntN(room/4)); err != nil {
				return err
			}
			time.Sleep(3 * time.Second)
			if _, err := m.guest("free 3"); err != nil {
				return err
			}
		}
		time.Sleep(time.Second)
		st := m.stat()
		peak = max(peak, st.vmas)
		if time.Now().After(next) {
			fmt.Printf("FCLAZY CHURN %s iter=%d peak_vmas=%d %s\n", id, i, peak, st)
			next = time.Now().Add(30 * time.Second)
		}
	}
	time.Sleep(10 * time.Second)
	st := m.stat()
	fmt.Printf("FCLAZY CHURN-END %s minutes=%.0f peak_vmas=%d max_map_count=%d %s\n", id, d.Minutes(), max(peak, st.vmas), maxMapCount(), st)
	return nil
}

func maxMapCount() int {
	data, _ := os.ReadFile("/proc/sys/vm/max_map_count")
	var v int
	fmt.Sscan(string(data), &v)
	return v
}

// chain boots one VM, saves it idle, then runs it through sleep and wake
// cycles. It returns the last saved state and whether every cycle passed.
func chain(name string, track bool, cycles int, churn time.Duration) (*saved, bool) {
	cfg := vmConfig{hv: "fc", mem: *mem, track: track}
	id := fmt.Sprintf("chain=%s mem=%d fs=%s track=%t merge=%s", name, *mem, *fsName, track, *merge)
	fail := func(what string, err error) (*saved, bool) {
		fmt.Printf("FCLAZY FAIL %s at=%s: %v\n", id, what, err)
		return nil, false
	}
	dir, err := newDir(name)
	if err != nil {
		return fail("dir", err)
	}
	base, state := filepath.Join(dir, "base.mem"), filepath.Join(dir, "base.state")
	diff, diffState, full := filepath.Join(dir, "diff.mem"), filepath.Join(dir, "diff.state"), filepath.Join(dir, "full.mem")

	m, ready, err := boot(dir, cfg)
	if err != nil {
		return fail("boot", err)
	}
	info, err := m.guest("info")
	if err != nil {
		m.kill()
		return fail("info", err)
	}
	e := &expect{gens: map[int][]int{stableID: make([]int, stableMiB()), heapID: make([]int, heapMiB)}, shmMiB: shmMiB(), bootID: field(info, "boot_id")}
	time.Sleep(8 * time.Second)
	_, _ = m.guest("prep")
	time.Sleep(3 * time.Second)
	st := m.stat()
	paused, diffTook, fullTook, err := m.sleep(base, state, full)
	alive := m.alive()
	if err != nil {
		tail := m.tail()
		m.kill()
		return fail("first sleep", fmt.Errorf("%w; %s", err, tail))
	}
	m.kill()
	c, err := compare(base, full, nil)
	if err != nil {
		return fail("first compare", err)
	}
	_, diffAlloc := sizeMiB(base)
	_, fullAlloc := sizeMiB(full)
	fmt.Printf("FCLAZY FIRST %s boot_ready_ms=%s vmm_alive=%t pause_ms=%s diff_ms=%s diff_allocated_mib=%d full_ms=%s full_allocated_mib=%d %s %s console_bad=%d\n",
		id, ms(ready), alive, ms(paused), ms(diffTook), diffAlloc, ms(fullTook), fullAlloc, c, st, printConsole(id, dir))
	_ = os.Remove(full)
	_ = os.Remove(full + ".state")

	pass := make([]bool, cycles+1)
	complete := false
	for cy := 1; cy <= cycles+1; cy++ {
		cache := "cold"
		if cy%2 == 0 {
			cache = "warm"
		}
		if err = setCache(cache, base, state); err != nil {
			return fail("cache", err)
		}
		var w wakeTimes
		if m, w, err = wake(dir, cfg, base, state); err != nil {
			fmt.Printf("FCLAZY WAKE %s cycle=%d cache=%s FAIL %v\n", id, cy, cache, err)
			break
		}
		line := fmt.Sprintf("FCLAZY WAKE %s cycle=%d cache=%s api_up_ms=%s resumed_ms=%s first_answer_ms=%s first_command_ms=%s", id, cy, cache, ms(w.apiUp), ms(w.resumed), ms(w.pong), ms(w.exec))
		key := fmt.Sprintf("WAKE %s cache=%s", id, cache)
		record(key+" metric=first_answer", w.pong)
		record(key+" metric=first_command", w.exec)
		if cy == 1 {
			fmt.Println(line)
			steps := []string{
				fmt.Sprintf("alloc %d %d", stableID, stableMiB()), fmt.Sprintf("alloc %d %d", heapID, heapMiB),
				fmt.Sprintf("fill %d 0 %d 1", stableID, stableMiB()), fmt.Sprintf("fill %d 0 %d 1", heapID, heapMiB),
				fmt.Sprintf("shm %d", shmMiB()), "wl 1", "disk 1",
			}
			for _, s := range steps {
				out, gerr := m.guest("%s", s)
				if gerr != nil {
					m.kill()
					return fail("setup", gerr)
				}
				if strings.HasPrefix(s, "shm") {
					e.shmSHA = field(out, "sha256")
				}
			}
			e.set(stableID, 0, stableMiB(), 1)
			e.set(heapID, 0, heapMiB, 1)
		} else {
			v, verr := verify(m, e)
			if verr != nil {
				fmt.Printf("%s verify FAIL %v; %s\n", line, verr, m.tail())
				m.kill()
				break
			}
			if cy == cycles+1 {
				// The loop was switched off for the last sleep.
				_, _ = m.guest("wl 1")
			}
			wl := workloadRuns(m)
			_, pingErr := sh("ping", "-c", "1", "-W", "2", "172.31.77.2")
			fmt.Printf("%s %s workload_running=%t net_ping_ok=%t verifies_sleep=%d\n", line, v, wl, pingErr == nil, cy-1)
			record(key+" metric=verify_all", v.took)
			pass[cy-1] = pass[cy-1] && v.pass() && wl
			if cy == cycles+1 {
				m.kill()
				complete = true
				break
			}
			off := (cy - 2) * 64 % (stableMiB() - 64)
			steps := []string{
				fmt.Sprintf("fill %d %d 64 %d", stableID, off, cy),
				fmt.Sprintf("free %d", heapID), "wait",
				fmt.Sprintf("alloc %d %d", heapID, heapMiB), fmt.Sprintf("fill %d 0 %d %d", heapID, heapMiB, cy),
				fmt.Sprintf("disk %d", cy),
			}
			for _, s := range steps {
				if s == "wait" {
					// Free page reporting starts two seconds after a free.
					time.Sleep(10 * time.Second)
					continue
				}
				if _, gerr := m.guest("%s", s); gerr != nil {
					m.kill()
					return fail("ops", gerr)
				}
			}
			e.set(stableID, off, 64, cy)
			e.set(heapID, 0, heapMiB, cy)
		}
		if cy == cycles {
			if churn > 0 {
				if err = churnLoop(m, id, churn); err != nil {
					fmt.Printf("FCLAZY CHURN %s FAIL %v; %s\n", id, err, m.tail())
				}
			}
			_, _ = m.guest("wl 0")
		}
		_, _ = m.guest("prep")
		time.Sleep(3 * time.Second)
		st = m.stat()
		paused, diffTook, fullTook, err = m.sleep(diff, diffState, full)
		alive = m.alive()
		if err != nil {
			fmt.Printf("FCLAZY SLEEP %s cycle=%d FAIL %v; %s\n", id, cy, err, m.tail())
			m.kill()
			break
		}
		m.kill()
		bad := printConsole(id, dir)
		_, diffAlloc = sizeMiB(diff)
		_, before := sizeMiB(base)
		var cpTook time.Duration
		target := base
		if *merge == "reflink" {
			target = filepath.Join(dir, "next.mem")
			_ = os.Remove(target)
			if cpTook, err = sh("cp", "--reflink=always", base, target); err != nil {
				return fail("reflink", err)
			}
		}
		mergeTook, syncTook, err := rebase(target, diff)
		if err != nil {
			return fail("rebase", err)
		}
		if target != base {
			if err = os.Rename(target, base); err != nil {
				return fail("rename", err)
			}
		}
		if err = os.Rename(diffState, state); err != nil {
			return fail("state", err)
		}
		_, after := sizeMiB(base)
		if c, err = compare(base, full, e); err != nil {
			return fail("compare", err)
		}
		changed := 64 + heapMiB + 32
		if cy == 1 {
			changed = stableMiB() + heapMiB + shmMiB() + 32
		}
		pass[cy] = alive && bad == 0 && c.mergedZero == 0 && c.other == 0 && c.missing == 0
		fmt.Printf("FCLAZY SLEEP %s cycle=%d vmm_alive=%t pause_ms=%s diff_ms=%s diff_allocated_mib=%d host_changed_mib=%d full_ms=%s reflink_ms=%s merge_ms=%s merge_fsync_ms=%s base_allocated_before_mib=%d base_allocated_after_mib=%d %s %s console_bad=%d\n",
			id, cy, alive, ms(paused), ms(diffTook), diffAlloc, changed, ms(fullTook), ms(cpTook), ms(mergeTook), ms(syncTook), before, after, c, st, bad)
		key = "SLEEP " + id
		if cy > 1 {
			record(key+" metric=pause", paused)
			record(key+" metric=diff_save", diffTook)
			record(key+" metric=full_save", fullTook)
			record(key+" metric=merge", mergeTook)
			record(key+" metric=merge_fsync", syncTook)
			if *merge == "reflink" {
				record(key+" metric=reflink_copy", cpTook)
			}
		}
		_ = os.Remove(diff)
		_ = os.Remove(full)
		_ = os.Remove(full + ".state")
	}
	passed := 0
	for cy := 1; cy <= cycles; cy++ {
		fmt.Printf("FCLAZY VERDICT %s cycle=%d pass=%t\n", id, cy, pass[cy])
		if pass[cy] {
			passed++
		}
	}
	fmt.Printf("FCLAZY RESULT %s cycles_passed=%d of=%d\n", id, passed, cycles)
	summarize()
	if !complete {
		return nil, false
	}
	return &saved{dir: dir, cfg: cfg, base: base, state: state, e: e}, passed == cycles
}

// timing wakes the saved VM without saving it again: first answer, first
// command, reading 600 MiB back and rewriting 400 MiB.
func timing(s *saved) {
	for i := 0; i < *n; i++ {
		for _, cache := range []string{"cold", "warm"} {
			for _, track := range []bool{true, false} {
				cfg := s.cfg
				cfg.track = track
				id := fmt.Sprintf("hv=fc-file mem=%d fs=%s track=%t cache=%s", *mem, *fsName, track, cache)
				if err := setCache(cache, s.base, s.state); err != nil {
					fmt.Printf("FCLAZY TIMING %s i=%d FAIL %v\n", id, i, err)
					continue
				}
				m, w, err := wake(s.dir, cfg, s.base, s.state)
				if err != nil {
					fmt.Printf("FCLAZY TIMING %s i=%d FAIL %v\n", id, i, err)
					continue
				}
				timeOps(m, id, i, w)
				m.kill()
			}
		}
	}
	printConsole("timing", s.dir)
	summarize()
}

func timeOps(m *vm, id string, i int, w wakeTimes) {
	t := time.Now()
	read, err := m.guest("touch %d 0 600", stableID)
	readTook := time.Since(t)
	var write string
	if err == nil {
		t = time.Now()
		write, err = m.guest("fill %d 0 400 %d", heapID, 5000+i)
	}
	if err != nil {
		fmt.Printf("FCLAZY TIMING %s i=%d FAIL %v; %s\n", id, i, err, m.tail())
		return
	}
	writeTook := time.Since(t)
	fmt.Printf("FCLAZY TIMING %s i=%d api_up_ms=%s resumed_ms=%s first_answer_ms=%s first_command_ms=%s read600_ms=%s write400_ms=%s total_ms=%s guest_read_us=%s guest_write_us=%s %s\n",
		id, i, ms(w.apiUp), ms(w.resumed), ms(w.pong), ms(w.exec), ms(readTook), ms(writeTook), ms(w.exec+readTook+writeTook), field(read, "us"), field(write, "us"), m.stat())
	key := "TIMING " + id
	record(key+" metric=first_answer", w.pong)
	record(key+" metric=first_command", w.exec)
	record(key+" metric=read600", readTook)
	record(key+" metric=write400", writeTook)
	record(key+" metric=total", w.exec+readTook+writeTook)
}

// idle wakes the saved VM from a cold file and samples the monitor's memory
// while the guest does nothing.
func idle(s *saved) {
	id := fmt.Sprintf("mem=%d fs=%s track=%t", *mem, *fsName, s.cfg.track)
	if err := setCache("cold", s.base, s.state); err != nil {
		fmt.Printf("FCLAZY IDLE %s FAIL %v\n", id, err)
		return
	}
	m, _, err := wake(s.dir, s.cfg, s.base, s.state)
	if err != nil {
		fmt.Printf("FCLAZY IDLE %s FAIL %v\n", id, err)
		return
	}
	defer m.kill()
	t := time.Now()
	for _, at := range []int{5, 60, 300} {
		time.Sleep(time.Until(t.Add(time.Duration(at) * time.Second)))
		fmt.Printf("FCLAZY IDLE %s at_s=%d %s\n", id, at, m.stat())
	}
}

// fork wakes a saved VM in dir, rewrites 64 MiB of the stable region at gen,
// and saves a Diff. It returns the diff's memory and state files.
func fork(dir string, cfg vmConfig, mem, state string, e *expect, off, gen int, tagName string) (string, string, error) {
	m, _, err := wake(dir, cfg, mem, state)
	if err != nil {
		return "", "", err
	}
	defer m.kill()
	if _, err = m.guest("fill %d %d 64 %d", stableID, off, gen); err != nil {
		return "", "", err
	}
	e.set(stableID, off, 64, gen)
	time.Sleep(time.Second)
	diff, diffState := filepath.Join(dir, tagName+".mem"), filepath.Join(dir, tagName+".state")
	_, _, _, err = m.sleep(diff, diffState, "")
	return diff, diffState, err
}

// wakeVerify wakes a memory file and reports what the guest's check finds.
func wakeVerify(dir string, cfg vmConfig, mem, state string, e *expect, during func() error) string {
	m, _, err := wake(dir, cfg, mem, state)
	if err != nil {
		return fmt.Sprintf("guest_wake=failed err=%q", err.Error())
	}
	defer m.kill()
	if during != nil {
		if err = during(); err != nil {
			return fmt.Sprintf("guest_wake=ok during_err=%q", err.Error())
		}
	}
	v, err := verify(m, e)
	if err != nil {
		return fmt.Sprintf("guest_wake=ok guest_verify=failed err=%q", err.Error())
	}
	return fmt.Sprintf("guest_wake=ok guest_verify_pass=%t %s", v.pass(), v)
}

// baseline proves the checker fails when it should: a chain with one diff
// left out, and a memory file changed under a running VM.
func baseline(s *saved) {
	id := fmt.Sprintf("mem=%d fs=%s track=%t", *mem, *fsName, s.cfg.track)
	fail := func(what string, err error) { fmt.Printf("FCLAZY BASELINE %s FAIL at=%s: %v\n", id, what, err) }
	dir := filepath.Join(*work, "fork")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("dir", err)
		return
	}
	if err := os.Link(*rootfs, filepath.Join(dir, "rootfs.ext4")); err != nil {
		fail("link", err)
		return
	}
	if _, err := copyFile(filepath.Join(s.dir, "data.raw"), filepath.Join(dir, "data.raw")); err != nil {
		fail("data", err)
		return
	}
	a0, a1, ctl, skip := filepath.Join(dir, "a0.mem"), filepath.Join(dir, "a1.mem"), filepath.Join(dir, "control.mem"), filepath.Join(dir, "skip.mem")
	if _, err := copyFile(s.base, a0); err != nil {
		fail("copy", err)
		return
	}
	e := s.e.clone()
	x, y := stableMiB()/2, stableMiB()/2+128
	dA, _, err := fork(dir, s.cfg, a0, s.state, e, x, 1001, "dA")
	if err != nil {
		fail("diff A", err)
		return
	}
	stateA := filepath.Join(dir, "dA.state")
	if _, err = copyFile(a0, a1); err == nil {
		_, _, err = rebase(a1, dA)
	}
	if err != nil {
		fail("merge A", err)
		return
	}
	dB, stateB, err := fork(dir, s.cfg, a1, stateA, e, y, 1002, "dB")
	if err != nil {
		fail("diff B", err)
		return
	}
	// Control: both diffs merged in order.
	if _, err = copyFile(a1, ctl); err == nil {
		_, _, err = rebase(ctl, dB)
	}
	if err != nil {
		fail("merge control", err)
		return
	}
	c, err := compare(ctl, "", e)
	if err != nil {
		fail("scan control", err)
		return
	}
	fmt.Printf("FCLAZY BASELINE %s round=control-in-order host_scan_missing=%d host_scan_stale_copies=%d %s\n", id, c.missing, c.staleCopies, wakeVerify(dir, s.cfg, ctl, stateB, e, nil))
	// Diff B merged without diff A.
	if _, err = copyFile(a0, skip); err == nil {
		_, _, err = rebase(skip, dB)
	}
	if err != nil {
		fail("merge skip", err)
		return
	}
	k, err := compare(skip, "", e)
	if err != nil {
		fail("scan skip", err)
		return
	}
	fmt.Printf("FCLAZY BASELINE %s round=skipped-diff host_scan_missing=%d host_scan_stale_copies=%d %s\n", id, k.missing, k.staleCopies, wakeVerify(dir, s.cfg, skip, stateB, e, nil))
	printConsole("baseline-skip", dir)
	// The memory file changed while a VM maps it: 1024 pattern pages the
	// woken guest has not touched are overwritten in the file.
	z := 16
	corrupted := 0
	during := func() error {
		f, oerr := os.OpenFile(ctl, os.O_WRONLY, 0)
		if oerr != nil {
			return oerr
		}
		defer f.Close()
		junk := make([]byte, pageSize)
		for i := range junk {
			junk[i] = 0xA5
		}
		for idx := z * 256; idx < (z+4)*256; idx++ {
			if off := c.stableOff[idx]; off >= 0 {
				if _, werr := f.WriteAt(junk, off); werr != nil {
					return werr
				}
				corrupted++
			}
		}
		return nil
	}
	out := wakeVerify(dir, s.cfg, ctl, stateB, e, during)
	fmt.Printf("FCLAZY BASELINE %s round=file-changed-while-mapped pages_overwritten=%d %s\n", id, corrupted, out)
	printConsole("baseline-corrupt", dir)
	_ = os.RemoveAll(dir)
}

// frag has the guest free every other page of most of its RAM and watches
// the monitor's mapping count against vm.max_map_count.
func frag(s *saved) {
	id := fmt.Sprintf("mem=%d fs=%s track=%t max_map_count=%d", *mem, *fsName, s.cfg.track, maxMapCount())
	m, _, err := wake(s.dir, s.cfg, s.base, s.state)
	if err != nil {
		fmt.Printf("FCLAZY FRAG %s FAIL %v\n", id, err)
		return
	}
	defer m.kill()
	size := (*mem - shmMiB()) * 70 / 100
	for _, step := range []string{fmt.Sprintf("free %d", stableID), fmt.Sprintf("free %d", heapID), fmt.Sprintf("frag 3 %d", size)} {
		if _, err = m.guest("%s", step); err != nil {
			fmt.Printf("FCLAZY FRAG %s step=%q FAIL %v; alive=%t; %s\n", id, step, err, m.alive(), m.tail())
			return
		}
	}
	t := time.Now()
	last, same := 0, 0
	for time.Since(t) < 6*time.Minute && m.alive() && same < 8 {
		time.Sleep(5 * time.Second)
		st := m.stat()
		fmt.Printf("FCLAZY FRAG %s frag_mib=%d at_s=%.0f vmm_alive=%t %s\n", id, size, time.Since(t).Seconds(), m.alive(), st)
		if st.vmas == last {
			same++
		} else {
			last, same = st.vmas, 0
		}
	}
	if !m.alive() {
		fmt.Printf("FCLAZY FRAG-END %s vmm_alive=false; %s\n", id, m.tail())
		return
	}
	_, perr := m.guest("ping")
	_, ferr := m.guest("free 3")
	time.Sleep(60 * time.Second)
	fmt.Printf("FCLAZY FRAG-END %s vmm_alive=%t guest_answers=%t freed=%t after_free_60s: %s\n", id, m.alive(), perr == nil, ferr == nil, m.stat())
	printConsole("frag", s.dir)
}

// chCow measures Cloud Hypervisor's copy-on-write restore of the same guest
// with the same host steps, for the comparison on this runner.
func chCow() {
	id := fmt.Sprintf("hv=ch-copyonwrite mem=%d fs=%s", *mem, *fsName)
	fail := func(what string, err error) { fmt.Printf("FCLAZY CHCOW %s FAIL at=%s: %v\n", id, what, err) }
	dir, err := newDir("ch")
	if err != nil {
		fail("dir", err)
		return
	}
	cfg := vmConfig{hv: "ch", mem: *mem}
	m, _, err := boot(dir, cfg)
	if err != nil {
		fail("boot", err)
		return
	}
	e := &expect{gens: map[int][]int{stableID: make([]int, stableMiB()), heapID: make([]int, heapMiB)}}
	for _, s := range []string{
		fmt.Sprintf("alloc %d %d", stableID, stableMiB()), fmt.Sprintf("alloc %d %d", heapID, heapMiB),
		fmt.Sprintf("fill %d 0 %d 1", stableID, stableMiB()), fmt.Sprintf("fill %d 0 %d 1", heapID, heapMiB),
		fmt.Sprintf("shm %d", shmMiB()), "info", "prep",
	} {
		out, gerr := m.guest("%s", s)
		if gerr != nil {
			m.kill()
			fail("setup", gerr)
			return
		}
		if strings.HasPrefix(s, "shm") {
			e.shmSHA = field(out, "sha256")
		}
		if s == "info" {
			e.bootID = field(out, "boot_id")
		}
	}
	e.set(stableID, 0, stableMiB(), 1)
	e.set(heapID, 0, heapMiB, 1)
	time.Sleep(3 * time.Second)
	snap := filepath.Join(dir, "snap")
	_ = os.MkdirAll(snap, 0o755)
	m.conn.Close()
	t := time.Now()
	if err = m.api(http.MethodPut, "/api/v1/vm.pause", nil); err == nil {
		err = m.api(http.MethodPut, "/api/v1/vm.snapshot", map[string]string{"destination_url": "file://" + snap})
	}
	took := time.Since(t)
	m.kill()
	if err != nil {
		fail("snapshot", err)
		return
	}
	var files []string
	var alloc int64
	entries, _ := os.ReadDir(snap)
	for _, en := range entries {
		files = append(files, filepath.Join(snap, en.Name()))
		_, a := sizeMiB(files[len(files)-1])
		alloc += a
	}
	fmt.Printf("FCLAZY CHCOW %s save_ms=%s allocated_mib=%d ch=%s\n", id, ms(took), alloc, chVersion())
	for i := 0; i <= *n; i++ {
		for _, cache := range []string{"cold", "warm"} {
			rid := id + " cache=" + cache
			if err = setCache(cache, files...); err != nil {
				fail("cache", err)
				return
			}
			m, w, werr := wake(dir, cfg, snap, "")
			if werr != nil {
				fmt.Printf("FCLAZY TIMING %s i=%d FAIL %v\n", rid, i, werr)
				continue
			}
			if i == 0 {
				v, verr := verify(m, e)
				fmt.Printf("FCLAZY CHCOW %s verify_before_timing: %s err=%v\n", rid, v, verr)
			} else {
				timeOps(m, rid, i, w)
			}
			m.kill()
		}
	}
	printConsole("chcow", dir)
	summarize()
	_ = os.RemoveAll(dir)
}

func chVersion() string {
	out, _ := exec.Command(*chBin, "--version").Output()
	return strings.ReplaceAll(strings.TrimSpace(string(out)), " ", "_")
}

func main() {
	flag.Parse()
	if err := os.MkdirAll(cgroupRoot, 0o755); err != nil {
		fmt.Println("FCLAZY WARN cgroup:", err)
	} else if err = os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+memory"), 0o644); err != nil {
		fmt.Println("FCLAZY WARN cgroup memory controller:", err)
	}
	fmt.Printf("FCLAZY PLAN mem=%d fs=%s merge=%s stable_mib=%d heap_mib=%d shm_mib=%d checked_mib=%d cycles=%d mincore_cycles=%d churn_min=%d n=%d max_map_count=%d\n",
		*mem, *fsName, *merge, stableMiB(), heapMiB, shmMiB(), stableMiB()+heapMiB+shmMiB(), *cycles, *mcCycles, *churnMin, *n, maxMapCount())
	has := func(s string) bool { return strings.Contains(","+*sections+",", ","+s+",") }
	if has("chain") {
		s, ok := chain("track", true, *cycles, time.Duration(*churnMin)*time.Minute)
		fmt.Printf("FCLAZY CHAIN-DONE chain=track mem=%d fs=%s all_passed=%t\n", *mem, *fsName, ok)
		if s != nil {
			if has("timing") {
				timing(s)
			}
			if has("idle") {
				idle(s)
			}
			if has("baseline") {
				baseline(s)
			}
			if has("frag") {
				frag(s)
			}
			_ = os.RemoveAll(s.dir)
		}
	}
	if has("mincore") {
		s, ok := chain("mincore", false, *mcCycles, 0)
		fmt.Printf("FCLAZY CHAIN-DONE chain=mincore mem=%d fs=%s all_passed=%t\n", *mem, *fsName, ok)
		if s != nil {
			if has("idle") {
				idle(s)
			}
			_ = os.RemoveAll(s.dir)
		}
	}
	if has("chcow") {
		chCow()
	}
	fmt.Println("FCLAZY DONE")
}
