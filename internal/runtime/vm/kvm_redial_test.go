//go:build kvm

package vm

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/guest"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMHelperIsNoticedWhenItListens boots VMs with two more clients
// waiting for the helper beside the engine's: one left to gRPC's reconnect
// backoff and one that redials as waitHelper does. Both wait through the
// same boot, so the gap between them is what the backoff cost.
func TestKVMHelperIsNoticedWhenItListens(t *testing.T) {
	e := kvmEngine(t)
	const boots = 6
	var gaps []time.Duration
	for i := range boots {
		key := fmt.Sprintf("run-vmtest-redial%d", i)
		t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			CPULimit: 1, MemoryLimitBytes: 512 << 20,
		}
		if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
			t.Fatal(err)
		}
		begin := time.Now()
		answered := func(redial bool) <-chan time.Duration {
			done := make(chan time.Duration, 1)
			c, err := guest.Dial(e.path(key, vsockSocket))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), bootTimeout)
			if redial {
				go func() {
					tick := time.NewTicker(helperRedial)
					defer tick.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-tick.C:
							c.Redial()
						}
					}
				}()
			}
			go func() {
				defer cancel()
				if _, err := c.Ping(ctx); err != nil {
					t.Errorf("ping %s: %v", key, err)
				}
				done <- time.Since(begin)
			}()
			return done
		}
		backoff, redial := answered(false), answered(true)
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		started := time.Since(begin)
		b, r := <-backoff, <-redial
		t.Logf("boot %d: start returned after %v; helper answered a redialing client after %v, a backoff client after %v (%v later)", i, started.Round(time.Millisecond), r.Round(time.Millisecond), b.Round(time.Millisecond), (b - r).Round(time.Millisecond))
		gaps = append(gaps, b-r)
		if err := e.destroy(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(gaps)
	t.Logf("backoff client behind the redialing one: median %v, min %v, max %v (n=%d)", gaps[boots/2].Round(time.Millisecond), gaps[0].Round(time.Millisecond), gaps[boots-1].Round(time.Millisecond), boots)
	if gaps[boots/2] < 0 {
		t.Errorf("the redialing client was the slower one in most boots: %v", gaps)
	}
}
