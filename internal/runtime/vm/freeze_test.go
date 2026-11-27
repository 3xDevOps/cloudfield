package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

func TestFreezeTakesARunningVMThatIsNotMidOperation(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"run-a", "run-b", "run-c"} {
		h.createStarted(key, sessionSpec(key))
	}
	if err := h.engine.stop(t.Context(), "run-c", 0); err != nil {
		t.Fatal(err)
	}
	if froze, err := h.engine.freeze("run-c"); froze || err == nil {
		t.Fatalf("freeze of a stopped VM = %v, %v", froze, err)
	}
	unlock := h.engine.lock("run-b")
	if froze, err := h.engine.freeze("run-b"); froze || err == nil {
		t.Fatalf("freeze of a VM mid-operation = %v, %v", froze, err)
	}
	unlock()
	for _, key := range []string{"run-a", "run-b"} {
		if froze, err := h.engine.freeze(key); !froze || err != nil {
			t.Fatalf("freeze %s = %v, %v", key, froze, err)
		}
	}
	if froze, err := h.engine.freeze("run-a"); froze || err != nil {
		t.Fatalf("freeze of a frozen VM = %v, %v", froze, err)
	}
	if data, err := os.ReadFile(h.engine.freezeFile("run-a")); err != nil || strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("the freezer of a frozen VM reads %q, %v", data, err)
	}
	if frozen := h.engine.frozenVMs(); !slices.Equal(frozen, []runtime.ID{idPrefix + "run-a", idPrefix + "run-b"}) {
		t.Fatalf("frozen = %v, want the longest frozen first", frozen)
	}
	if err := h.engine.thaw("run-a"); err != nil {
		t.Fatal(err)
	}
	// A new engine, as after a server restart, still finds what is frozen.
	if frozen := h.newEngine(h.engine.cfg.DataDir).frozenVMs(); !slices.Equal(frozen, []runtime.ID{idPrefix + "run-b"}) {
		t.Fatalf("after a thaw and a restart frozen = %v", frozen)
	}
	if err := h.engine.stop(t.Context(), "run-b", 0); err != nil {
		t.Fatalf("stop a frozen VM: %v", err)
	}
}

func TestSaveOfAFrozenVMPausesItWithoutLettingItsGuestRun(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	if froze, err := h.engine.freeze("run-a"); err != nil || !froze {
		t.Fatalf("freeze = %v, %v", froze, err)
	}
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vmm.called(), []string{"vm.info", "vm.pause", "vm.snapshot", "vmm.shutdown"}) {
		t.Errorf("save calls = %v", vmm.called())
	}
	if g := h.guest("run-a"); g.syncs != 0 {
		t.Errorf("the save synced a frozen guest %d times; a sync lets it run", g.syncs)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if h.engine.frozen("run-a") {
		t.Error("the restored VM is frozen")
	}
}

func TestAFailedSaveLeavesTheVMRunning(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	vmm.mu.Lock()
	vmm.snapshotFails = true
	vmm.mu.Unlock()
	if err := h.engine.save(t.Context(), "run-a"); err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("save = %v, want the snapshot's error", err)
	}
	if h.engine.halted(t.Context(), "run-a") {
		t.Fatal("a failed save left the VM paused")
	}
}

// The snapshot's page cache is charged to the unit: its memory limit is off
// while the monitor writes, and back before a failed save's guest runs on.
func TestASaveLiftsTheUnitsMemoryLimitForTheSnapshot(t *testing.T) {
	h := newHarness(t)
	limit := func(key string) string {
		data, err := os.ReadFile(filepath.Join(h.units.cgroup(unitName(key)), "memory.max"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	h.createStarted("run-a", sessionSpec("run-a"))
	hold := make(chan struct{})
	vmm := h.units.vmm("run-a")
	vmm.mu.Lock()
	vmm.snapshotHold = hold
	vmm.mu.Unlock()
	saved := make(chan error, 1)
	go func() { saved <- h.engine.save(t.Context(), "run-a") }()
	<-hold
	if got := limit("run-a"); got != "max" {
		t.Errorf("during the snapshot the unit's memory.max is %q, want max", got)
	}
	hold <- struct{}{}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}

	h.createStarted("run-b", sessionSpec("run-b"))
	vmm = h.units.vmm("run-b")
	vmm.mu.Lock()
	vmm.snapshotFails = true
	vmm.mu.Unlock()
	if err := h.engine.save(t.Context(), "run-b"); err == nil {
		t.Fatal("a save whose snapshot fails returned no error")
	}
	rec, err := h.engine.load("run-b")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := limit("run-b"), strconv.FormatInt(unitMemoryMiB(rec)<<20, 10); got != want {
		t.Errorf("after a failed save the unit's memory.max is %q, want %s", got, want)
	}
}

// A save or park whose context ends while the guest still runs gives up
// there: it neither pauses the guest nor syncs it again.
func TestASleepGivenUpBeforeThePauseLeavesTheVMRunning(t *testing.T) {
	sleeps := map[string]func(*engine, context.Context, string) error{"save": (*engine).save, "park": (*engine).park}
	for name, sleep := range sleeps {
		t.Run(name+" during the wait for freed pages", func(t *testing.T) {
			h := newHarness(t)
			h.createStarted("run-a", sessionSpec("run-a"))
			h.engine.reportPoll = time.Hour
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			if err := sleep(h.engine, ctx, "run-a"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s = %v, want its context's end", name, err)
			}
			if calls := h.units.vmm("run-a").called(); slices.Contains(calls, "vm.pause") {
				t.Errorf("a %s given up before the pause called %v", name, calls)
			}
			if g := h.guest("run-a"); g.syncs > 1 {
				t.Errorf("the %s synced the guest %d times after it was given up", name, g.syncs)
			}
		})
		t.Run(name+" during the first sync", func(t *testing.T) {
			h := newHarness(t)
			h.createStarted("run-a", sessionSpec("run-a"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g := h.guest("run-a")
			g.mu.Lock()
			g.onSync, g.syncErr = cancel, context.Canceled
			g.mu.Unlock()
			if err := sleep(h.engine, ctx, "run-a"); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s = %v, want its context's end", name, err)
			}
			if calls := h.units.vmm("run-a").called(); slices.Contains(calls, "vm.pause") {
				t.Errorf("a %s given up during its sync called %v", name, calls)
			}
		})
	}
}

// A snapshot runs to its end however long it takes: cut short, the monitor
// would go on writing it with the VM paused.
func TestASnapshotOutlastsTheSavesContext(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	hold := make(chan struct{})
	vmm.mu.Lock()
	vmm.snapshotHold = hold
	vmm.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	saved := make(chan error, 1)
	go func() { saved <- h.engine.save(ctx, "run-a") }()
	<-hold
	cancel()
	hold <- struct{}{}
	if err := <-saved; err != nil {
		t.Fatalf("a save whose context ended during the snapshot = %v", err)
	}
	if rec, err := h.engine.load("run-a"); err != nil || !rec.Saved {
		t.Fatalf("after the snapshot the record is %+v, %v; want it saved", rec, err)
	}
}
