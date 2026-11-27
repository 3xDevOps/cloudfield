//go:build kvm

package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMGuestMemoryUsesHugePages boots an 8 GiB VM under the host policy
// never and under the one adviseHugePages sets: with it the host backs the
// guest's memory with huge pages, without holding more of it, and the boot
// costs less CPU. A wake loads the guest back onto 4 KiB pages, so it holds
// what it saved. The log has both boots' times.
func TestKVMGuestMemoryUsesHugePages(t *testing.T) {
	e := kvmEngine(t)
	unitStat := func(key, file, name string) uint64 {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", runtime.SessionSlice, unitName(key), file))
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, name+" "); ok {
				return parseUint(t, v)
			}
		}
		t.Fatalf("%s of %s has no %s", file, key, name)
		return 0
	}
	boot := func(key string) time.Duration {
		t.Helper()
		t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 8 << 30,
		}
		if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		return time.Since(began)
	}
	// The medians of three boots: how long the start took, the CPU the unit
	// used, and the guest memory the host held, in all and in huge pages.
	measure := func(policy string) (time.Duration, uint64, uint64, uint64) {
		t.Helper()
		var starts []time.Duration
		var cpu, held, huge []uint64
		for i := range 3 {
			key := fmt.Sprintf("run-hugepages-%s-%d", policy, i)
			starts = append(starts, boot(key))
			cpu = append(cpu, unitStat(key, "cpu.stat", "usage_usec"))
			held = append(held, unitStat(key, "memory.stat", "shmem"))
			huge = append(huge, unitStat(key, "memory.stat", "shmem_thp"))
			if err := e.destroy(t.Context(), key); err != nil {
				t.Fatal(err)
			}
		}
		slices.Sort(starts)
		slices.Sort(cpu)
		slices.Sort(held)
		slices.Sort(huge)
		return starts[1], cpu[1], held[1], huge[1]
	}

	if err := os.WriteFile(shmemHugePages, []byte("never"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainStart, plainCPU, plainHeld, plainHuge := measure("never")
	if was, err := adviseHugePages(shmemHugePages); err != nil || was != "never" {
		t.Fatalf("adviseHugePages found %q: %v", was, err)
	}
	start, cpu, held, huge := measure("advise")
	t.Logf("an 8 GiB VM booted in %s on 4 KiB pages and %s on huge pages (medians of 3); the unit used %d and %d ms of CPU and the host held %d and %d MiB of the guest, %d and %d MiB of that in huge pages",
		plainStart.Round(time.Millisecond), start.Round(time.Millisecond), plainCPU/1000, cpu/1000, plainHeld>>20, held>>20, plainHuge>>20, huge>>20)
	if plainHuge != 0 {
		t.Errorf("under never the host backed %d bytes of the guest with huge pages", plainHuge)
	}
	if huge < held/2 {
		t.Errorf("under advise the host backed %d of the guest's %d bytes with huge pages, want most", huge, held)
	}
	if held > plainHeld+plainHeld/10 {
		t.Errorf("a booted VM holds %d bytes of the host on huge pages, over 10%% more than the %d it holds on 4 KiB pages", held, plainHeld)
	}
	if cpu > plainCPU*3/4 {
		t.Errorf("the boot used %d us of CPU on huge pages, want under three quarters of the %d on 4 KiB pages", cpu, plainCPU)
	}

	const key = "run-hugepages-wake"
	boot(key)
	if err := e.save(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := e.restore(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if woken := unitStat(key, "memory.stat", "shmem_thp"); woken != 0 {
		t.Errorf("the host backs %d bytes of a woken guest with huge pages, want none", woken)
	}
}
