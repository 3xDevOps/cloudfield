//go:build kvm

package vm

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMIdleVMPausedInMemoryAnswersAtOnce pauses a session VM as the sleep
// sweep leaves an idle one, in the host's memory, and resumes it: its first
// command must return in a fraction of a restore's time, with the guest's
// processes, memory and clock as a running VM has them. It then saves the
// paused VM as the memory guard does, frozen, and restores it: the session
// must come back with the same processes and memory. Both kinds of pause
// run: a VM with a checkout disk has its session's processes frozen, one
// without has its vCPUs stopped.
func TestKVMIdleVMPausedInMemoryAnswersAtOnce(t *testing.T) {
	h := newCheckoutHost(t)
	ws := domain.WorkspaceID("wsresident")
	base := h.workspace(t, ws, 20)
	frozen := fmt.Sprintf("run-vmresident-%x", time.Now().Unix())
	t.Cleanup(func() { _ = h.e.destroy(context.Background(), frozen) })
	t.Cleanup(func() { _ = h.git.RemoveRunHistory(context.Background(), domain.RunID(frozen)) })
	checkout, _, err := h.git.CreateRunCheckoutAt(t.Context(), ws, domain.RunID(frozen), base, "main", "a task", "")
	if err != nil {
		t.Fatal(err)
	}
	h.boot(t, frozen, checkout, "0:0")
	halted := "run-vmresident-halted"
	kvmSession(t, h.e, halted)

	const alive = "pgrep -x sleep | sort | tr '\\n' ' '; cat /proc/sys/kernel/random/boot_id"
	for _, key := range []string{frozen, halted} {
		e := h.e
		id := runtime.ID(idPrefix + key)
		sum, _ := vmRun(t, e, key, "head -c 200M /dev/urandom >/dev/shm/fill; setsid sleep 4000 </dev/null >/dev/null 2>&1 & cksum </dev/shm/fill")
		before, _ := vmRun(t, e, key, alive)
		held := func() uint64 {
			use, useErr := e.memoryUse()
			if useErr != nil {
				t.Fatal(useErr)
			}
			return use[id]
		}

		start := time.Now()
		if err := e.pause(t.Context(), key); err != nil {
			t.Fatalf("%s: pause: %v", key, err)
		}
		pauseTook := time.Since(start)
		holds := held()
		if holds == 0 {
			t.Fatalf("%s: a paused VM counts no memory, so admission and the memory guard would not see what it holds", key)
		}
		// Long enough for a clock that stood still to show.
		time.Sleep(5 * time.Second)
		start = time.Now()
		if err := e.resume(t.Context(), key); err != nil {
			t.Fatalf("%s: resume: %v", key, err)
		}
		resumeTook := time.Since(start)
		out, code := vmRun(t, e, key, "date +%s.%N")
		woke := time.Since(start)
		guest, parseErr := strconv.ParseFloat(strings.TrimSpace(out), 64)
		if code != 0 || parseErr != nil {
			t.Fatalf("%s: the first command after the resume exited %d with %q", key, code, out)
		}
		lag := float64(time.Now().UnixNano())/1e9 - guest
		t.Logf("%s: pause %s; paused it held %d MiB; resume %s, resume to the first command's return %s; guest clock %.2f s behind the host",
			key, pauseTook.Round(100*time.Microsecond), holds>>20, resumeTook.Round(100*time.Microsecond), woke.Round(100*time.Microsecond), lag)
		if woke > time.Second {
			t.Errorf("%s: a VM paused in memory took %s to answer its first command", key, woke)
		}
		if math.Abs(lag) > 2 {
			t.Errorf("%s: the guest's clock is %.2f s off the host's after 5 s paused", key, lag)
		}
		if after, _ := vmRun(t, e, key, alive); after != before {
			t.Fatalf("%s: after the resume the guest has processes and boot %q, want %q", key, after, before)
		}

		if err := e.pause(t.Context(), key); err != nil {
			t.Fatalf("%s: pause again: %v", key, err)
		}
		if froze, err := e.freeze(key); !froze || err != nil {
			t.Fatalf("%s: freeze = %v, %v", key, froze, err)
		}
		start = time.Now()
		if err := e.save(t.Context(), key); err != nil {
			t.Fatalf("%s: save a VM paused in memory: %v", key, err)
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
		t.Logf("%s: saved paused in %s (%d MiB), restored and resumed in %s (resume: %v)", key, saveTook.Round(time.Millisecond), saved>>20, time.Since(start).Round(time.Millisecond), resumeErr)
		if after, _ := vmRun(t, e, key, alive); after != before {
			t.Fatalf("%s: after the restore the guest has processes and boot %q, want %q%s", key, after, before, e.logTail(key))
		}
		if got, _ := vmRun(t, e, key, "cksum </dev/shm/fill"); got != sum {
			t.Fatalf("%s: the guest's memory reads back as %q, want %q", key, got, sum)
		}
	}
}
