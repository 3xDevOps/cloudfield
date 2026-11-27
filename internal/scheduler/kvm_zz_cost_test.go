//go:build kvm

package scheduler

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
)

// Throwaway measurement for PR #1199; never merged.
func TestKVMZZCost(t *testing.T) {
	for i := range 6 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			e, run, cid := kvmEnhancedSession(t)
			ms := func(d time.Duration) string { return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) }
			guest := func(script string) string {
				_, out, _, err := e.cfg.Runtime.Exec(t.Context(), cid, []string{"/bin/sh", "-c", script}, "")
				if err != nil {
					t.Fatal(err)
				}
				return strings.Join(strings.Fields(out), " ")
			}
			const where = `for p in /proc/[0-9]*; do if [ "$(cat "$p/comm" 2>/dev/null)" = acp-mock ]; then cat "$p/cgroup" "$p/oom_score_adj"; fi; done; cat /sys/fs/cgroup/session/memory.current`

			start := time.Now()
			kvmTurn(t, e, run.ID, 2, acpmock.PromptRun+"echo hi")
			t.Logf("COST first_command_ms=%s agent=%q", ms(time.Since(start)), guest(where))

			const cost = `t() { s=$(date +%s%N); for i in $(seq 200); do "$@"; done; echo "$(( ($(date +%s%N) - s) / 200000 ))"; }; t bash -c true; t /run/aether-acp-command true`
			said, _ := kvmTurn(t, e, run.ID, 3, acpmock.PromptRun+cost)
			t.Logf("COST command_us plain,wrapped=%q", strings.Join(strings.Fields(said), ","))

			flags := func() string {
				e.sched.mu.Lock()
				defer e.sched.mu.Unlock()
				entry := e.sched.runs[run.ID]
				return "saved=" + strconv.FormatBool(entry.asleepSaved) + " resident=" + strconv.FormatBool(entry.asleepResident)
			}
			e.sched.cfg.VMSleepAfter = time.Nanosecond
			turn := 4
			for _, save := range []bool{false, true} {
				start = time.Now()
				e.sleepRun(t, run.ID)
				slept := time.Since(start)
				if save {
					start = time.Now()
					e.sched.vmMemoryMu.Lock()
					_, err := e.sched.savePausedVMs(t.Context(), math.MaxUint64)
					e.sched.vmMemoryMu.Unlock()
					if err != nil {
						t.Logf("COST save error: %v", err)
					}
					slept += time.Since(start)
				}
				state := flags()
				start = time.Now()
				if err := e.sched.WakeRun(t.Context(), run.ID, e.member.ID); err != nil {
					t.Fatalf("wake: %v", err)
				}
				woke := time.Since(start)
				kvmTurn(t, e, run.ID, turn, "after the wake")
				turn++
				t.Logf("COST wake %s sleep_ms=%s wake_ms=%s wake_to_answer_ms=%s agent=%q", state, ms(slept), ms(woke), ms(time.Since(start)), guest(where))
			}
		})
	}
}
