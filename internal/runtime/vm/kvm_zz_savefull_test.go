//go:build kvm

package vm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Scratch, never merged: 40 saves of a full 2 GiB VM, each followed by a
// wake and a check of the guest's memory. A VM whose save or wake fails is
// made again. Prints "SAVEFULL ..." lines.
func TestKVMZZSaveFull(t *testing.T) {
	e := kvmEngine(t)
	key := "run-zz-save-full"
	ctx := context.Background()
	t.Cleanup(func() { _ = e.destroy(ctx, key) })
	const saves = 40
	var sum []string
	up, passed, killed := false, 0, 0
	for n := 1; n <= saves; n++ {
		if !up {
			_ = e.destroy(ctx, key)
			sum = fullSession(t, e, key)
			up = true
		}
		held := guestRAMOnHost(t, key)
		start := time.Now()
		reached, err := saveCountingLimit(e, key)
		took := time.Since(start)
		if err != nil {
			kills := oomKills()
			if strings.Contains(kills, "cloud-hyperv") {
				killed++
			}
			fmt.Printf("SAVEFULL n=%d ok=false step=save held_mib=%d at_limit=%d save_ms=%d err=%q dmesg=%q\n", n, held>>20, reached, took.Milliseconds(), err, kills)
			up = false
			continue
		}
		saved, _ := e.savedMemory(key)
		start = time.Now()
		if err = e.restore(ctx, key); err != nil {
			fmt.Printf("SAVEFULL n=%d ok=false step=restore held_mib=%d at_limit=%d err=%q\n", n, held>>20, reached, err)
			up = false
			continue
		}
		restored := time.Since(start)
		code, out, _, err := e.exec(ctx, key, []string{"/bin/sh", "-c", "cksum </dev/shm/fill"}, "")
		ok := err == nil && code == 0 && strings.Join(strings.Fields(out), " ") == strings.Join(sum, " ")
		if ok {
			passed++
		}
		fmt.Printf("SAVEFULL n=%d ok=%v held_mib=%d at_limit=%d save_ms=%d saved_mib=%d restore_ms=%d cksum=%q err=%v\n",
			n, ok, held>>20, reached, took.Milliseconds(), saved>>20, restored.Milliseconds(), strings.TrimSpace(out), err)
	}
	fmt.Printf("SAVEFULL-SUMMARY saves=%d passed=%d monitor_killed=%d\n", saves, passed, killed)
	if passed != saves {
		t.Errorf("%d of %d saves passed", passed, saves)
	}
}
