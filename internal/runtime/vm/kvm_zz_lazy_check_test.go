//go:build kvm

package vm

import (
	"bytes"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scratch code for the lazy-restore experiment, never merged: the host
// side's page compare and the early reader of a new monitor's guest RAM.

type lazyRegion struct {
	start, end, off uint64
	perms, path     string
}

// lazyRAMMaps is every writable mapping of at least 256 MiB in pid, which
// in Cloud Hypervisor is guest RAM alone.
func lazyRAMMaps(pid int) []lazyRegion {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil
	}
	var regions []lazyRegion
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
		regions = append(regions, lazyRegion{start, end, off, f[1], strings.Join(f[5:], " ")})
	}
	return regions
}

func lazyRegionsString(regions []lazyRegion) string {
	parts := make([]string, len(regions))
	for i, r := range regions {
		parts[i] = fmt.Sprintf("%x-%x(%dMiB) %s off=%#x %q", r.start, r.end, (r.end-r.start)>>20, r.perms, r.off, r.path)
	}
	return strings.Join(parts, "; ")
}

// lazyCHPid is Cloud Hypervisor's process in a VM unit's control group.
func lazyCHPid(cgroup string) int {
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

type lazyInjector struct {
	stop  chan struct{}
	done  chan struct{}
	mu    sync.Mutex
	lines []string
	// offs are the offsets in the memory file of the pages read while
	// they held zeros.
	offs []uint64
}

// lazyInject reads a new monitor's guest RAM through /proc/<pid>/mem as
// soon as the mapping shows in /proc/<pid>/maps, as a host agent that
// scans new processes does: one page at the start of each RAM region and
// 64 at random offsets, again each time the mappings change.
func lazyInject(cgroup string, began time.Time) *lazyInjector {
	in := &lazyInjector{stop: make(chan struct{}), done: make(chan struct{})}
	fractions := make([]float64, 64)
	for i := range fractions {
		fractions[i] = mrand.Float64()
	}
	note := func(format string, args ...any) {
		in.mu.Lock()
		in.lines = append(in.lines, fmt.Sprintf(format, args...))
		in.mu.Unlock()
	}
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
				note("INJ no cloud-hypervisor process seen")
				return
			}
			pid = lazyCHPid(cgroup)
			goruntime.Gosched()
		}
		seen := time.Since(began)
		mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
		if err != nil {
			note("INJ open mem: %v", err)
			return
		}
		defer func() { _ = mem.Close() }()
		var last string
		page := make([]byte, 4096)
		zeroPage := make([]byte, 4096)
		for round := 0; round < 8 && !stopped(); {
			regions := lazyRAMMaps(pid)
			sig := lazyRegionsString(regions)
			if len(regions) == 0 || sig == last {
				if last == "" {
					goruntime.Gosched()
				} else {
					time.Sleep(200 * time.Microsecond)
				}
				continue
			}
			last = sig
			round++
			at := time.Since(began)
			var total uint64
			for _, r := range regions {
				total += r.end - r.start
			}
			type target struct{ addr, off uint64 }
			var targets []target
			var cumulative uint64
			for _, r := range regions {
				fileOff := cumulative
				if r.path != "" {
					fileOff = r.off
				}
				targets = append(targets, target{r.start, fileOff})
				for _, f := range fractions {
					pos := uint64(f*float64(total)) &^ 4095
					if pos >= cumulative && pos < cumulative+r.end-r.start {
						targets = append(targets, target{r.start + pos - cumulative, fileOff + pos - cumulative})
					}
				}
				cumulative += r.end - r.start
			}
			var zero, nonzero, failed int
			var firstErr error
			var offs []uint64
			for _, tg := range targets {
				if _, err := mem.ReadAt(page, int64(tg.addr)); err != nil {
					failed++
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				if bytes.Equal(page, zeroPage) {
					zero++
					offs = append(offs, tg.off)
				} else {
					nonzero++
				}
			}
			in.mu.Lock()
			if round == 1 {
				in.offs = offs
			}
			in.mu.Unlock()
			note("INJ round=%d pid=%d pid_seen_ms=%.2f maps_seen_ms=%.2f done_ms=%.2f reads=%d zero=%d nonzero=%d err=%d first_err=%v regions=[%s]",
				round, pid, ms(seen), ms(at), ms(time.Since(began)), len(targets), zero, nonzero, failed, firstErr, sig)
		}
	}()
	return in
}

// end stops the injector and returns its report and the memory file
// offsets its first round read as zeros.
func (in *lazyInjector) end() ([]string, map[uint64]bool) {
	if in == nil {
		return nil, nil
	}
	close(in.stop)
	select {
	case <-in.done:
	case <-time.After(30 * time.Second):
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	offs := map[uint64]bool{}
	for _, off := range in.offs {
		offs[off] = true
	}
	return append([]string{}, in.lines...), offs
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

type lazyDiffPage struct {
	off                uint64
	origZero, restZero bool
	bytes              int
	explained          bool
	detail             string
}

type lazyCompareResult struct {
	pages                         uint64
	origBytes, restBytes          int64
	differ, unexplained           int
	restZero, restZeroUnexplained int
	origZero                      int
	list                          []lazyDiffPage
}

// lazyCompare compares two memory files page by page; a hole reads as
// zeros. explained holds, by page offset, the byte range in that page the
// host writes by itself; a page that differs only there is explained.
func lazyCompare(orig, rest string, explained map[uint64][2]int) (lazyCompareResult, error) {
	var res lazyCompareResult
	a, err := os.Open(orig)
	if err != nil {
		return res, err
	}
	defer func() { _ = a.Close() }()
	b, err := os.Open(rest)
	if err != nil {
		return res, err
	}
	defer func() { _ = b.Close() }()
	ai, _ := a.Stat()
	bi, _ := b.Stat()
	res.origBytes, res.restBytes = ai.Size(), bi.Size()
	if ai.Size() != bi.Size() {
		return res, fmt.Errorf("the memory files differ in length: %d and %d bytes", ai.Size(), bi.Size())
	}
	const chunk = 4 << 20
	bufA, bufB := make([]byte, chunk), make([]byte, chunk)
	zero := make([]byte, 4096)
	for off := int64(0); off < ai.Size(); off += chunk {
		n := int(min(chunk, ai.Size()-off))
		if _, err = io.ReadFull(io.NewSectionReader(a, off, int64(n)), bufA[:n]); err != nil {
			return res, err
		}
		if _, err = io.ReadFull(io.NewSectionReader(b, off, int64(n)), bufB[:n]); err != nil {
			return res, err
		}
		res.pages += uint64(n / 4096)
		if bytes.Equal(bufA[:n], bufB[:n]) {
			continue
		}
		for p := 0; p+4096 <= n; p += 4096 {
			pa, pb := bufA[p:p+4096], bufB[p:p+4096]
			if bytes.Equal(pa, pb) {
				continue
			}
			d := lazyDiffPage{off: uint64(off) + uint64(p), origZero: bytes.Equal(pa, zero), restZero: bytes.Equal(pb, zero)}
			span, known := explained[d.off]
			d.explained = known
			var detail []string
			for i := range pa {
				if pa[i] != pb[i] {
					d.bytes++
					if i < span[0] || i >= span[1] {
						d.explained = false
					}
					if len(detail) < 72 {
						detail = append(detail, fmt.Sprintf("%03x:%02x>%02x", i, pa[i], pb[i]))
					}
				}
			}
			d.detail = strings.Join(detail, " ")
			res.differ++
			if d.restZero {
				res.restZero++
			}
			if d.origZero {
				res.origZero++
			}
			if !d.explained {
				res.unexplained++
				if d.restZero {
					res.restZeroUnexplained++
				}
			}
			if len(res.list) < 2000 {
				res.list = append(res.list, d)
			}
		}
	}
	return res, nil
}
