//go:build kvm

package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMSaveOfAFullVMNeverReachesTheUnitsMemoryLimit saves a VM whose
// session holds 1800 MiB of its 2 GiB. The snapshot's page cache is charged
// to the VM's unit: held to the unit's MemoryMax, the write reached it in
// every save, and in 7 of 80 the kernel then killed Cloud Hypervisor
// ("Memory cgroup out of memory: Killed process ... (cloud-hyperviso)"),
// which lost the session's memory.
func TestKVMSaveOfAFullVMNeverReachesTheUnitsMemoryLimit(t *testing.T) {
	e := kvmEngine(t)
	key := "run-save-full"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	sum := fullSession(t, e, key)
	held := guestRAMOnHost(t, key)
	reached, err := saveCountingLimit(e, key)
	if err != nil {
		t.Fatalf("the save of a VM that held %d MiB of the host: %v\n%s", held>>20, err, oomKills())
	}
	if reached > 0 {
		t.Errorf("the save of a VM that held %d MiB of the host took its unit to its memory limit %d times", held>>20, reached)
	}
	if err = e.restore(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if got := guestFields(t, e, key, "cksum </dev/shm/fill", 2); got[0] != sum[0] || got[1] != sum[1] {
		t.Errorf("the restored guest reads its memory back as %v, want %v", got, sum)
	}
}

// fullSession starts a 2 GiB session VM that holds 1800 MiB in /dev/shm and
// returns that file's checksum.
func fullSession(t *testing.T, e *engine, key string) []string {
	t.Helper()
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 2, MemoryLimitBytes: 2 << 30,
	}
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return guestFields(t, e, key, "mount -o remount,size=90% /dev/shm && head -c 1800M /dev/urandom >/dev/shm/fill && cksum </dev/shm/fill", 2)
}

func oomKills() string {
	out, _ := exec.Command("sh", "-c", "dmesg | grep -i 'out of memory' | tail -2").CombinedOutput()
	return string(out)
}

// saveCountingLimit saves key's VM and returns how often its unit's memory
// reached the unit's limit meanwhile: the rise of max in memory.events,
// read until the save ends the unit.
func saveCountingLimit(e *engine, key string) (uint64, error) {
	events := filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(key), "memory.events")
	atLimit := func() (uint64, bool) {
		data, _ := os.ReadFile(events)
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "max "); ok {
				n, err := strconv.ParseUint(v, 10, 64)
				return n, err == nil
			}
		}
		return 0, false
	}
	before, _ := atLimit()
	stop, last := make(chan struct{}), make(chan uint64)
	go func() {
		seen := before
		for {
			select {
			case <-stop:
				last <- seen
				return
			case <-time.After(10 * time.Millisecond):
			}
			if n, ok := atLimit(); ok {
				seen = n
			}
		}
	}()
	err := e.save(context.Background(), key)
	close(stop)
	return <-last - before, err
}
