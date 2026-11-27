//go:build kvm

package vm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

const zzInstalled = "/usr/local/bin/cloud-hypervisor"

func zzSwap(t *testing.T, from string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(zzInstalled+".new", data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(zzInstalled+".new", zzInstalled); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(zzInstalled, "--version").CombinedOutput()
	t.Logf("REVERSEPIN installed now: %s %v", strings.TrimSpace(string(out)), err)
}

// Throwaway: a VM saved by Cloud Hypervisor v55.0 is woken by v54.1, as an
// asleep session is after a rollback.
func TestKVMZZRestoreAcrossPin(t *testing.T) {
	old := os.Getenv("AETHER_ZZ_CH54")
	if old == "" {
		t.Skip("set AETHER_ZZ_CH54")
	}
	e := kvmEngine(t)
	pinned := t.TempDir() + "/ch55"
	data, err := os.ReadFile(zzInstalled)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(pinned, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { zzSwap(t, pinned) })
	const alive = "pgrep -x sleep | sort | tr '\\n' ' '; cat /proc/sys/kernel/random/boot_id"
	const addrs = "ip -o addr show | tr '\\n' ';'"
	for i := range 3 {
		key := fmt.Sprintf("run-zzpin-%d", i)
		zzSwap(t, pinned)
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 2, MemoryLimitBytes: 2 << 30,
		}
		if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
			t.Fatal(err)
		}
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		sum, _ := vmRun(t, e, key, "mount -o remount,size=90% /dev/shm; head -c 400M /dev/urandom >/dev/shm/fill; "+
			"setsid sleep 4000 </dev/null >/dev/null 2>&1 & cksum </dev/shm/fill")
		before, _ := vmRun(t, e, key, alive)
		net, _ := vmRun(t, e, key, addrs)
		if err := e.save(t.Context(), key); err != nil {
			t.Fatalf("save on v55.0: %v", err)
		}
		zzSwap(t, old)
		for cycle := range 2 {
			if err := e.restore(t.Context(), key); err != nil {
				t.Errorf("REVERSEPIN vm=%d cycle=%d restore FAILED: %v%s", i, cycle, err, e.logTail(key))
				break
			}
			after, _ := vmRun(t, e, key, alive)
			got, _ := vmRun(t, e, key, "cksum </dev/shm/fill")
			netAfter, _ := vmRun(t, e, key, addrs)
			st, stErr := e.sample(t.Context(), key)
			ok := after == before && got == sum && netAfter == net && stErr == nil
			t.Logf("REVERSEPIN vm=%d cycle=%d ok=%v processes_and_boot_same=%v memory_same=%v net_same=%v stats_err=%v stats=%+v",
				i, cycle, ok, after == before, got == sum, netAfter == net, stErr, st)
			if !ok {
				t.Errorf("REVERSEPIN vm=%d cycle=%d mismatch%s", i, cycle, e.logTail(key))
				break
			}
			if cycle == 0 {
				if err := e.save(t.Context(), key); err != nil {
					t.Errorf("REVERSEPIN vm=%d save on v54.1 FAILED: %v", i, err)
					break
				}
			}
		}
		_ = e.destroy(context.Background(), key)
	}
}
