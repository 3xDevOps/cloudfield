//go:build kvm

package vm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// TestKVMSavesAVMAnOlderServerLeftPaused saves a session VM that is already
// paused, as the first start after an update finds the VMs of a release that
// paused idle ones in the host's memory, and restores it: the session must
// come back with the same processes and memory. Recovery saves such a VM as
// it stands, and the memory guard freezes it first. Both kinds of pause run:
// a VM with a checkout disk has its session's processes frozen, one without
// has its vCPUs stopped.
func TestKVMSavesAVMAnOlderServerLeftPaused(t *testing.T) {
	h := newCheckoutHost(t)
	ws := domain.WorkspaceID("wstakeover")
	base := h.workspace(t, ws, 20)
	frozen := fmt.Sprintf("run-vmtakeover-%x", time.Now().Unix())
	t.Cleanup(func() { _ = h.e.destroy(context.Background(), frozen) })
	t.Cleanup(func() { _ = h.git.RemoveRunHistory(context.Background(), domain.RunID(frozen)) })
	checkout, _, err := h.git.CreateRunCheckoutAt(t.Context(), ws, domain.RunID(frozen), base, "main", "a task", "")
	if err != nil {
		t.Fatal(err)
	}
	h.boot(t, frozen, checkout, "0:0")
	halted := "run-vmtakeover-halted"
	kvmSession(t, h.e, halted)

	const alive = "pgrep -x sleep | sort | tr '\\n' ' '; cat /proc/sys/kernel/random/boot_id"
	e := h.e
	for _, key := range []string{frozen, halted} {
		sum, _ := vmRun(t, e, key, "head -c 200M /dev/urandom >/dev/shm/fill; setsid sleep 4000 </dev/null >/dev/null 2>&1 & cksum </dev/shm/fill")
		before, _ := vmRun(t, e, key, alive)
		for _, saves := range []string{"recovery", "the memory guard"} {
			if err := e.pause(t.Context(), key); err != nil {
				t.Fatalf("%s: pause: %v", key, err)
			}
			if saves == "the memory guard" {
				if froze, err := e.freeze(key); !froze || err != nil {
					t.Fatalf("%s: freeze = %v, %v", key, froze, err)
				}
			}
			start := time.Now()
			if err := e.save(t.Context(), key); err != nil {
				t.Fatalf("%s: save a paused VM as %s does: %v", key, saves, err)
			}
			saveTook := time.Since(start)
			saved, _ := e.savedMemory(key)
			start = time.Now()
			if err := e.restore(t.Context(), key); err != nil {
				t.Fatalf("%s: restore: %v", key, err)
			}
			// Only a session saved frozen comes back paused; the scheduler
			// resumes either without minding a refusal.
			resumeErr := e.resume(t.Context(), key)
			t.Logf("%s: saved paused as %s does in %s (%d MiB), restored and resumed in %s (resume: %v)",
				key, saves, saveTook.Round(time.Millisecond), saved>>20, time.Since(start).Round(time.Millisecond), resumeErr)
			if after, _ := vmRun(t, e, key, alive); after != before {
				t.Fatalf("%s: after the restore the guest has processes and boot %q, want %q%s", key, after, before, e.logTail(key))
			}
			if got, _ := vmRun(t, e, key, "cksum </dev/shm/fill"); got != sum {
				t.Fatalf("%s: the guest's memory reads back as %q, want %q", key, got, sum)
			}
		}
	}
}
