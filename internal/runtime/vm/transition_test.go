package vm

import (
	"testing"
)

// Cloud Hypervisor refuses to resume a VM that runs and to pause one that
// is paused. The server's record and the monitor part when a server stops
// between a call and writing it down, or an operator resumes a VM at its
// socket: the call that brings them back together must not fail on it.
func TestResumeAndPauseAcceptAVMAlreadyInThatState(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := h.engine.resume(t.Context(), "run-a"); err != nil {
		t.Fatalf("resume of a VM that already runs = %v", err)
	}
	if err := h.engine.pause(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.pause(t.Context(), "run-a"); err != nil {
		t.Fatalf("pause of a VM that is already paused = %v", err)
	}
	if !h.engine.halted(t.Context(), "run-a") {
		t.Fatal("the VM runs after two pauses")
	}
	if err := h.engine.resume(t.Context(), "run-a"); err != nil || h.engine.halted(t.Context(), "run-a") {
		t.Fatalf("resume of the paused VM = %v, still paused %t", err, h.engine.halted(t.Context(), "run-a"))
	}

	// A held session whose vCPUs were started at the socket: its session is
	// still frozen, and resume thaws it.
	h.createWithCheckoutDisk("run-b")
	if err := h.engine.pause(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	vmm := h.units.vmm("run-b")
	vmm.mu.Lock()
	vmm.state = "Running"
	vmm.mu.Unlock()
	if err := h.engine.resume(t.Context(), "run-b"); err != nil || h.guest("run-b").isSessionFrozen() {
		t.Fatalf("resume of a held VM whose vCPUs already run = %v, session frozen %t", err, h.guest("run-b").isSessionFrozen())
	}
}
