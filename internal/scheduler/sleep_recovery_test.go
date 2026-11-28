package scheduler

import (
	"bytes"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// leavePaused leaves the VM cid as a release that paused idle VMs in memory
// left it: recorded asleep, with asleep_resident in its sidecar, and paused.
func (e *testEnv) leavePaused(t *testing.T, mr *memoryRuntime, cid runtime.ID) domain.RunID {
	t.Helper()
	run := e.runOf(t, cid)
	waitItems(t, e.sched, run, "the task's turn", turnEnded("end_turn", 1))
	e.sched.mu.Lock()
	entry := e.sched.runs[run]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	path := e.sched.sidecarPath(run)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("{"), []byte(`{"asleep_resident":true,`), 1)
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = mr.Pause(t.Context(), cid); err != nil {
		t.Fatal(err)
	}
	return run
}

// waitSleepSaved waits for the run.sleep event that says run sleeps with
// every process.
func waitSleepSaved(t *testing.T, sub events.Subscription, run domain.RunID) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed while waiting for run.sleep")
			}
			if p, isSleep := ev.Payload.(events.RunSleepPayload); isSleep && ev.RunID == run && p.AsleepSince != nil && p.AsleepSaved {
				return
			}
		case <-deadline:
			t.Fatalf("no run.sleep event said %s sleeps with every process", run)
		}
	}
}

// A release that paused idle VMs in memory left such a VM paused, with
// asleep_resident in its sidecar. Recovery saves it only when its memory
// fits on the data disk, and the next prompt restores it. One that does not
// fit stays paused and reads as asleep with every process: a prompt resumes
// it, and the memory guard saves it once the disk has room. Either way a
// run.sleep event says so once recovery settled it.
func TestRecoverySavesAVMAnOlderServerLeftPausedOnlyWhenItFitsOnDisk(t *testing.T) {
	// A 100 GiB disk has room for 1 GiB with 80 GiB free and none with 9.5,
	// as in TestTheMemoryGuardSkipsSavesThatFailedOrWouldNotFitOnDisk.
	const room, full = 80 << 30, 19 << 29
	const restored, resumed = "had saved this session to disk and restored it", "had left this sleeping session's VM running, not saved to disk"
	cases := map[string]struct {
		free       uint64
		guardSaves bool
		notice     string
	}{
		"with room, recovery saves it":                        {free: room, notice: restored},
		"with no room, a prompt resumes it":                   {free: full, notice: resumed},
		"with no room, the guard saves it once there is some": {free: full, guardSaves: true, notice: restored},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, mr := newMemoryEnv(t)
			cid := e.launchVM(t, mr, "say pong", 1<<30)
			run := e.leavePaused(t, mr, cid)
			var free atomic.Uint64
			free.Store(tc.free)
			e.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
				return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free.Load()}, nil
			}

			sub := e.subscribe(t)
			s2 := e.restartACP(t)
			waitFor(t, "the paused VM read as asleep with every process", func() bool { return s2.Sleep(run).Saved })
			waitSleepSaved(t, sub, run)
			fits := tc.free == room
			if state := containerState(e.container(t, run)); mr.isSaved(cid) != fits || !fits && (mr.saveCount() != 0 || state != "paused") {
				t.Fatalf("recovery left the VM saved %v after %d saves and %s; want saved %v, and paused after no save when not",
					mr.isSaved(cid), mr.saveCount(), state, fits)
			}
			if tc.guardSaves {
				free.Store(room)
				mr.setAvailable(3 << 30)
				s2.guardVMMemory(t.Context())
				if !mr.isSaved(cid) || !s2.Sleep(run).Saved {
					t.Fatal("the guard did not save the VM recovery left paused once the disk had room")
				}
				mr.setAvailable(24 << 30)
			}
			if _, err := s2.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: "after the update"}, false, nil); err != nil {
				t.Fatalf("a prompt to the session: %v", err)
			}
			if state := containerState(e.container(t, run)); state != "running" || mr.isSaved(cid) {
				t.Fatalf("the prompt left the VM %s, saved %v; want running", state, mr.isSaved(cid))
			}
			waitItems(t, s2, run, "a turn after the wake", turnEnded("end_turn", 2))
			waitItems(t, s2, run, "how the session woke", noticed("Woke from sleep", tc.notice))
			if !adopted(mr.acpRuntime) {
				t.Fatalf("the wake left %d agents, want the one the session had, on the conversation it had", len(mr.all()))
			}
		})
	}
}

// Recovery takes up its runs at once. With room on the data disk for the
// memory of one of two paused VMs only, it saves one and leaves the other
// paused: a save that has yet to write counts against the room of the next.
func TestRecoverySavesOnlyThePausedVMsThatFitOnDiskTogether(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	cids := []runtime.ID{e.launchVM(t, mr, "say pong", 1<<30), e.launchVM(t, mr, "say pong", 1<<30)}
	var runs []domain.RunID
	for _, cid := range cids {
		runs = append(runs, e.leavePaused(t, mr, cid))
	}
	savedVMs := func() (n uint64) {
		for _, cid := range cids {
			if mr.isSaved(cid) {
				n++
			}
		}
		return n
	}
	// 10.5 GiB free of 100 GiB has room for 1 GiB once, and a save takes
	// its GiB as it ends.
	var measured atomic.Int64
	e.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		measured.Add(1)
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 21<<29 - savedVMs()<<30}, nil
	}
	// The first save is held until the disk was measured for both VMs.
	started, release := make(chan struct{}, len(cids)), make(chan struct{})
	mr.saveMu.Lock()
	mr.saving, mr.release = started, release
	mr.saveMu.Unlock()
	go func() {
		defer close(release)
		for len(started) == 0 || measured.Load() < int64(len(cids)) {
			select {
			case <-t.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	s2 := e.restartACP(t)
	if mr.saveCount() != 1 || savedVMs() != 1 {
		t.Fatalf("recovery started %d saves and saved %d VMs, want one of the two", mr.saveCount(), savedVMs())
	}
	for i, cid := range cids {
		if !s2.Sleep(runs[i]).Saved {
			t.Fatalf("%s does not read as asleep with every process", runs[i])
		}
		if state := containerState(e.container(t, runs[i])); !mr.isSaved(cid) && state != "paused" {
			t.Fatalf("the VM recovery did not save is %s, want paused", state)
		}
	}
}
