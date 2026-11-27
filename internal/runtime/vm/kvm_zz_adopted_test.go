//go:build kvm

package vm

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
)

// Scratch code of scratch/lazy-adopted-chain, never merged: the rewrite of
// a saved configuration to private memory, the early reader of a new
// monitor's guest RAM, and the stale-base control.

// zzPrivateConfig rewrites the VM configuration saved in dir to private
// guest memory.
func zzPrivateConfig(dir string) (was, now string, err error) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var cfg map[string]any
	if err = dec.Decode(&cfg); err != nil {
		return "", "", err
	}
	memory, ok := cfg["memory"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("saved configuration has no memory: %.200s", data)
	}
	before, _ := json.Marshal(memory)
	memory["shared"] = false
	after, _ := json.Marshal(memory)
	if data, err = json.Marshal(cfg); err != nil {
		return "", "", err
	}
	return string(before), string(after), os.WriteFile(path, data, 0o600)
}

type zzRegion struct {
	start, end, off uint64
	perms, path     string
}

// zzRAMMaps is every writable mapping of at least 256 MiB in pid, which in
// Cloud Hypervisor is guest RAM alone.
func zzRAMMaps(pid int) []zzRegion {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil
	}
	var regions []zzRegion
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[1], "rw") {
			continue
		}
		from, to, _ := strings.Cut(f[0], "-")
		start, err1 := strconv.ParseUint(from, 16, 64)
		end, err2 := strconv.ParseUint(to, 16, 64)
		off, err3 := strconv.ParseUint(f[2], 16, 64)
		if err1 != nil || err2 != nil || err3 != nil || end-start < 256<<20 {
			continue
		}
		regions = append(regions, zzRegion{start, end, off, f[1], strings.Join(f[5:], " ")})
	}
	return regions
}

func zzCHPid(cgroup string) int {
	data, err := os.ReadFile(filepath.Join(cgroup, "cgroup.procs"))
	if err != nil {
		return 0
	}
	for field := range strings.FieldsSeq(string(data)) {
		comm, err := os.ReadFile("/proc/" + field + "/comm")
		if err == nil && strings.HasPrefix(string(comm), "cloud-hyperv") {
			pid, _ := strconv.Atoi(field)
			return pid
		}
	}
	return 0
}

type zzInjector struct {
	stop, done chan struct{}
	// Read after end.
	lines                       []string
	rounds, reads, zero, failed int
	firstMapsMS                 float64
}

// zzInject reads a new monitor's guest RAM through /proc/<pid>/mem as soon
// as the mapping shows in /proc/<pid>/maps, as a host agent that scans new
// processes does: one page at the start of each RAM region and 64 at random
// offsets, again each time the mappings change. It is the early reader of
// scratch/lazy-ch-cow-correctness.
func zzInject(cgroup string, began time.Time) *zzInjector {
	in := &zzInjector{stop: make(chan struct{}), done: make(chan struct{})}
	fractions := make([]float64, 64)
	for i := range fractions {
		fractions[i] = mrand.Float64()
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	stopped := func() bool {
		select {
		case <-in.stop:
			return true
		default:
			return false
		}
	}
	go func() {
		defer close(in.done)
		pid := 0
		for pid == 0 {
			if stopped() {
				in.lines = append(in.lines, "no cloud-hypervisor process seen")
				return
			}
			pid = zzCHPid(cgroup)
			goruntime.Gosched()
		}
		seen := time.Since(began)
		mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
		if err != nil {
			in.lines = append(in.lines, fmt.Sprintf("open mem: %v", err))
			return
		}
		defer func() { _ = mem.Close() }()
		var last string
		page := make([]byte, 4096)
		zeroPage := make([]byte, 4096)
		for in.rounds < 8 && !stopped() {
			regions := zzRAMMaps(pid)
			parts := make([]string, len(regions))
			for i, r := range regions {
				parts[i] = fmt.Sprintf("%x-%x(%dMiB) %s off=%#x %q", r.start, r.end, (r.end-r.start)>>20, r.perms, r.off, r.path)
			}
			sig := strings.Join(parts, "; ")
			if len(regions) == 0 || sig == last {
				if last == "" {
					goruntime.Gosched()
				} else {
					time.Sleep(200 * time.Microsecond)
				}
				continue
			}
			last = sig
			in.rounds++
			at := time.Since(began)
			if in.rounds == 1 {
				in.firstMapsMS = ms(at)
			}
			var total uint64
			for _, r := range regions {
				total += r.end - r.start
			}
			var targets []uint64
			var cumulative uint64
			for _, r := range regions {
				targets = append(targets, r.start)
				for _, f := range fractions {
					pos := uint64(f*float64(total)) &^ 4095
					if pos >= cumulative && pos < cumulative+r.end-r.start {
						targets = append(targets, r.start+pos-cumulative)
					}
				}
				cumulative += r.end - r.start
			}
			var zero, nonzero, failed int
			var firstErr error
			for _, addr := range targets {
				if _, err := mem.ReadAt(page, int64(addr)); err != nil {
					failed++
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				if bytes.Equal(page, zeroPage) {
					zero++
				} else {
					nonzero++
				}
			}
			in.reads += len(targets)
			in.zero += zero
			in.failed += failed
			in.lines = append(in.lines, fmt.Sprintf("round=%d pid=%d pid_seen_ms=%.2f maps_seen_ms=%.2f done_ms=%.2f reads=%d zero=%d nonzero=%d err=%d first_err=%v regions=[%s]",
				in.rounds, pid, ms(seen), ms(at), ms(time.Since(began)), len(targets), zero, nonzero, failed, firstErr, sig))
		}
	}()
	return in
}

func (in *zzInjector) end() {
	close(in.stop)
	<-in.done
}

var zzGamma uint64 = 0x9E3779B97F4A7C15

func zzMix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	return x ^ x>>31
}

// revertPages puts back, in the memory file fresh, what the older file
// stale holds at every page of the checker's pattern that carries
// generation gen and that want selects by its page index.
func revertPages(fresh, stale string, gen uint64, want func(idx uint64) bool) (reverted, wasOlderPattern int, err error) {
	nf, err := os.OpenFile(fresh, os.O_RDWR, 0)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = nf.Close() }()
	of, err := os.Open(stale)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = of.Close() }()
	buf, old := make([]byte, 4<<20), make([]byte, 4096)
	for at := int64(0); ; {
		n, readErr := io.ReadFull(io.NewSectionReader(nf, at, int64(len(buf))), buf)
		for p := 0; p+4096 <= n; p += 4096 {
			idx := binary.NativeEndian.Uint64(buf[p+8:])
			if binary.NativeEndian.Uint64(buf[p:]) != gen || !want(idx) ||
				binary.NativeEndian.Uint64(buf[p+16:]) != zzMix(idx*zzGamma+gen)+2*zzGamma {
				continue
			}
			off := at + int64(p)
			if _, err = of.ReadAt(old, off); err != nil {
				return reverted, wasOlderPattern, err
			}
			if binary.NativeEndian.Uint64(old[8:]) == idx && binary.NativeEndian.Uint64(old) != gen {
				wasOlderPattern++
			}
			if _, err = nf.WriteAt(old, off); err != nil {
				return reverted, wasOlderPattern, err
			}
			reverted++
		}
		at += int64(n)
		if readErr != nil {
			return reverted, wasOlderPattern, nf.Sync()
		}
	}
}

// staleControl is the failing control: one more cycle whose saved memory
// file then gets pages of the file before it put back, as a save that
// cloned a stale base or skipped changed pages would leave it. The host
// page compare and the guest check must both report it. The true file is
// put back afterwards.
func (z *zz) staleControl(snapDir, nextDir string, gen uint64) {
	t := z.t
	z.wake("control", false)
	if !z.verify("after wake control") {
		t.Fatalf("the guest check failed before the control%s", z.e.logTail(z.key))
	}
	picked := z.rewrite(gen, 64)
	z.settle("control")
	if err := os.Mkdir(nextDir, 0o700); err != nil {
		t.Fatal(err)
	}
	z.staleBase = filepath.Join(snapDir, "memory-ranges")
	_, err := z.snapshot("control", nextDir, true, 64)
	z.staleBase = ""
	if err != nil {
		t.Fatalf("control save: %v%s", err, z.e.logTail(z.key))
	}
	z.sleep()

	fresh, kept := filepath.Join(nextDir, "memory-ranges"), filepath.Join(nextDir, "memory-ranges.true")
	if out, cpErr := exec.Command("cp", "--reflink=always", fresh, kept).CombinedOutput(); cpErr != nil {
		t.Fatalf("keep the true memory file: %v: %s", cpErr, out)
	}
	// Eight whole chunks, and one page inside a ninth.
	whole := map[uint64]bool{}
	for _, c := range picked[:8] {
		whole[uint64(c)] = true
	}
	lone := uint64(picked[8])*256 + 7
	reverted, older, err := revertPages(fresh, filepath.Join(snapDir, "memory-ranges"), gen, func(idx uint64) bool {
		return whole[idx/256] || idx == lone
	})
	z.logf("control stale pages put_back=%d of_which_older_pattern=%d want_at_least=%d chunks=%v lone_page=%d err=%v", reverted, older, 8*256+1, picked[:8], lone, err)
	if err != nil || reverted < 8*256+1 {
		t.Fatalf("control setup: %d pages put back: %v", reverted, err)
	}
	if err = errors.Join(os.RemoveAll(snapDir), os.Rename(nextDir, snapDir)); err != nil {
		t.Fatal(err)
	}
	z.syncfs()

	counter, chain := z.counter, z.chain
	z.quiet, z.caught = true, nil
	z.wake("control stale", false)
	ok := z.verify("after wake control stale")
	z.quiet = false
	z.logf("control stale result caught=%v reports=%d", !ok, len(z.caught))
	if ok {
		t.Errorf("ZZ fs=%s mib=%d CONTROL-MISSED: the guest check passed on a memory file with %d stale pages", z.fs, z.mib, reverted)
	}
	z.sleep()
	if err = os.Rename(filepath.Join(snapDir, "memory-ranges.true"), filepath.Join(snapDir, "memory-ranges")); err != nil {
		t.Fatal(err)
	}
	z.syncfs()
	z.counter, z.chain = counter, chain
}
