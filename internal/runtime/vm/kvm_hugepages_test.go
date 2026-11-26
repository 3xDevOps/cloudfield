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
// costs less CPU. The log has both boots' times.
func TestKVMGuestMemoryUsesHugePages(t *testing.T) {
	e := kvmEngine(t)
	boot := func(policy string, i int) (start time.Duration, cpuUsec, shmem, huge uint64) {
		t.Helper()
		key := fmt.Sprintf("run-hugepages-%s-%d", policy, i)
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
		start = time.Since(began)
		cgroup := filepath.Join("/sys/fs/cgroup", runtime.SessionSlice, unitName(key))
		stat := func(file, name string) uint64 {
			data, err := os.ReadFile(filepath.Join(cgroup, file))
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
		cpuUsec, shmem, huge = stat("cpu.stat", "usage_usec"), stat("memory.stat", "shmem"), stat("memory.stat", "shmem_thp")
		if err := e.destroy(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		return start, cpuUsec, shmem, huge
	}
	const boots = 3
	measure := func(policy string) (start time.Duration, cpuUsec, shmem, huge uint64) {
		var starts []time.Duration
		var cpus, shmems, huges []uint64
		for i := range boots {
			s, c, m, h := boot(policy, i)
			starts, cpus, shmems, huges = append(starts, s), append(cpus, c), append(shmems, m), append(huges, h)
		}
		slices.Sort(starts)
		slices.Sort(cpus)
		slices.Sort(shmems)
		slices.Sort(huges)
		return starts[boots/2], cpus[boots/2], shmems[boots/2], huges[boots/2]
	}

	if err := os.WriteFile(shmemHugePages, []byte("never"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainStart, plainCPU, plainMem, plainHuge := measure("never")
	if was, err := adviseHugePages(shmemHugePages); err != nil || was != "never" {
		t.Fatalf("adviseHugePages found %q: %v", was, err)
	}
	start, cpu, mem, huge := measure("advise")
	t.Logf("an 8 GiB VM booted in %s on 4 KiB pages and %s on huge pages (medians of %d); the unit used %d and %d ms of CPU and the host held %d and %d MiB of the guest, %d and %d MiB of that in huge pages",
		plainStart.Round(time.Millisecond), start.Round(time.Millisecond), boots, plainCPU/1000, cpu/1000, plainMem>>20, mem>>20, plainHuge>>20, huge>>20)
	if plainHuge != 0 {
		t.Errorf("under never the host backed %d bytes of the guest with huge pages", plainHuge)
	}
	if huge < mem/2 {
		t.Errorf("under advise the host backed %d of the guest's %d bytes with huge pages, want most", huge, mem)
	}
	if mem > plainMem+plainMem/10 {
		t.Errorf("a booted VM holds %d bytes of the host on huge pages, over 10%% more than the %d it holds on 4 KiB pages", mem, plainMem)
	}
	if cpu > plainCPU*3/4 {
		t.Errorf("the boot used %d us of CPU on huge pages, want under three quarters of the %d on 4 KiB pages", cpu, plainCPU)
	}
}
