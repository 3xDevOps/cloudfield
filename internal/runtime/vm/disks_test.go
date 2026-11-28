package vm

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// A session takes no more of the host through /tmp and nested Docker than
// its data disk holds. Its swap file is added to the disk, so a larger
// memory limit leaves the session the same room, and root keeps a share of
// the disk. The session's database disk stays the session's whole.
func TestANewVMsDataDiskHoldsTheSessionsRoomAndItsSwap(t *testing.T) {
	h := newHarness(t)
	state := t.TempDir()
	small, large := sessionSpec("run-a"), sessionSpec("run-b")
	small.Mounts = []runtime.Mount{{HostPath: state, Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState}}
	large.MemoryLimitBytes = 64 << 30
	for _, vm := range []struct {
		spec runtime.Spec
		opts options
	}{{small, options{}}, {large, options{}}, {sessionSpec("run-c"), options{NoSwap: true}}} {
		if _, err := h.engine.create(t.Context(), vm.spec.CreationKey, vm.spec, vm.opts); err != nil {
			t.Fatal(err)
		}
	}
	want := []fakeDataDisk{
		{h.engine.path("run-a", dataDisk), 32<<30 + 512<<20, 5},
		{filepath.Join(state, stateDiskFile), 1 << 30, 0},
		{h.engine.path("run-b", dataDisk), 32<<30 + 16<<30, 5},
		{h.engine.path("run-c", dataDisk), 32 << 30, 5},
	}
	if !slices.Equal(h.images.dataDisks, want) {
		t.Errorf("the VMs' data disks, each with its size and root's percent:\n got %+v\nwant %+v", h.images.dataDisks, want)
	}
}

// A data disk's size is its file's, set when the VM was created: a VM from
// before a server update keeps the disk it has through a stop and start, a
// sleep and wake, and a server restart.
func TestAnExistingVMKeepsItsDataDisk(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	disk := h.engine.path("run-a", dataDisk)
	const before = 128 << 30
	if err := os.Truncate(disk, before); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	after := h.newEngine(filepath.Dir(h.engine.root))
	if err := after.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := after.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(disk); err != nil || info.Size() != before || len(h.images.dataDisks) != 1 {
		t.Errorf("after a stop, a sleep and a server restart: data disk %v, %v, with %d made; want the one of %d bytes", info, err, len(h.images.dataDisks), int64(before))
	}
	if got := launchDisks(t, h.units.last().launch.Args)["data"]["path"]; got != disk {
		t.Errorf("the VM started on the data disk %q, want %s", got, disk)
	}
}

func TestDiskUseCountsTheBlocksOfEachVMsDisks(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.createStarted("run-b", sessionSpec("run-b"))
	for _, name := range []string{rootDisk, dataDisk} {
		if err := os.WriteFile(h.engine.path("run-a", name), bytes.Repeat([]byte{1}, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	use, err := h.engine.diskUse()
	if err != nil {
		t.Fatal(err)
	}
	if a, b := use[runtime.ID(idPrefix+"run-a")], use[runtime.ID(idPrefix+"run-b")]; a < 2<<20 || b >= 1<<20 || len(use) != 2 {
		t.Errorf("disk use = %v; want 2 MiB for run-a's root and data disks and nothing for run-b", use)
	}
}

func TestHoldDisksStopsAFrozenVMUntilItsResume(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-b")
	if err := h.engine.pause(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if !h.engine.halted(t.Context(), "run-b") || !h.guest("run-b").isSessionFrozen() {
		t.Fatal("a held VM runs on, or its session thawed")
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Errorf("holding a held VM again = %v", err)
	}
	if err := h.engine.resume(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if h.engine.halted(t.Context(), "run-b") || h.guest("run-b").isSessionFrozen() {
		t.Error("resume left the held VM stopped or its session frozen")
	}
}

// A VM the disk guard holds still runs, so a capture only starts its vCPUs:
// no second boot, and its session stays frozen.
func TestBootForCaptureStartsTheVCPUsOfAHeldVM(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-b")
	if err := h.engine.pause(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-b")
	boots := len(g.boots)
	if err := (&Runtime{vms: h.engine}).BootForCapture(t.Context(), runtime.ID(idPrefix+"run-b")); err != nil {
		t.Fatal(err)
	}
	if h.engine.halted(t.Context(), "run-b") || !g.isSessionFrozen() || len(g.boots) != boots {
		t.Errorf("after the capture boot: halted %v, frozen %v, boots %d, want %d", h.engine.halted(t.Context(), "run-b"), g.isSessionFrozen(), len(g.boots), boots)
	}
}

// A saved VM is restored for capture, not started from its disks: its saved
// memory may hold writes its disks do not. Its session stays frozen, so the
// agent a sleep saved running does not work on.
func TestBootForCaptureRestoresASavedVM(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-a")
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-a")
	boots := len(g.boots)
	if err := (&Runtime{vms: h.engine}).BootForCapture(t.Context(), runtime.ID(idPrefix+"run-a")); err != nil {
		t.Fatal(err)
	}
	rec, err := h.engine.load("run-a")
	if err != nil || rec.Saved || len(g.boots) != boots || !g.isSessionFrozen() {
		t.Errorf("after the capture boot: %v, saved %v, boots %d (want %d), frozen %v; want the VM restored with its session frozen",
			err, rec != nil && rec.Saved, len(g.boots), boots, g.isSessionFrozen())
	}
}
