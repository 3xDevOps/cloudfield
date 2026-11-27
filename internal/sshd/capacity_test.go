package sshd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
)

type machineRuns struct {
	RunController
	status scheduler.MachineStatus
	vms    domain.VMCounts
}

func (m machineRuns) Machine(context.Context) scheduler.MachineStatus { return m.status }
func (m machineRuns) VMs() domain.VMCounts                            { return m.vms }

func TestServerCapacityReportsTheMachineAndWhatWasCounted(t *testing.T) {
	t.Parallel()
	stall := 12.5
	status := scheduler.MachineStatus{
		MemoryTotalBytes: 32 << 30, MemoryAvailableBytes: 1 << 30, MemoryFloorBytes: 3 << 30, StartMemoryBytes: 512 << 20,
		ServerStall: &stall, ServerStallLimit: 10, HostStallLimit: 60, StallError: "host pressure: unreadable",
		Filesystems:    []scheduler.MachineFilesystem{{Name: "Cloudfield data filesystem", TotalBytes: 100 << 30, FreeBytes: 4 << 30, ReserveBytes: 5 << 30}},
		StartDiskBytes: 1 << 30,
	}
	e := newScopedTestEnv(t, func(c *Config) {
		c.Runs = machineRuns{RunController: c.Runs, status: status, vms: domain.VMCounts{Awake: 3, Frozen: 1, Paused: 2, Saved: 4}}
	})
	_, member := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	now := time.Now().UTC()
	for _, ev := range []domain.CapacityEvent{
		{At: now.Add(-10 * 24 * time.Hour), Kind: domain.CapacityRefusal, Op: "launch", Cause: "memory", Run: "run-old", Text: "an old refusal", UsedBytes: 100, IdleHourBytes: 20},
		{At: now.Add(-time.Minute), Kind: domain.CapacityRefusal, Op: "launch-retry", Cause: "memory", Run: "run-new", Text: "scheduler: insufficient host memory headroom: 1 available bytes", UsedBytes: 100, IdleHourBytes: 60},
		{At: now.Add(-2 * time.Minute), Kind: domain.CapacityRefusal, Op: "wake", Cause: "disk", Run: "run-asleep", Text: "scheduler: insufficient disk headroom"},
		{At: now.Add(-time.Minute), Kind: domain.CapacityWaitEnd, Op: "subsession", Cause: "started", Waited: 90 * time.Second},
		{At: now.Add(-20 * 24 * time.Hour), Kind: domain.CapacitySave, Op: "idle", VMBytes: 7},
	} {
		if err := e.db.CountCapacityEvent(t.Context(), ev); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id domain.MemberID) protocol.ServerCapacityResult {
		t.Helper()
		raw, err := e.srv.Local(id).Call(t.Context(), protocol.MethodServerCapacity, nil)
		if err != nil {
			t.Fatal(err)
		}
		var result protocol.ServerCapacityResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	got := read(e.member.ID)
	if got.MemoryAvailableBytes != 1<<30 || got.MemoryFloorBytes != 3<<30 || got.ServerStall == nil || *got.ServerStall != 12.5 || got.HostStall != nil ||
		got.StallError != "host pressure: unreadable" || len(got.Disks) != 1 || got.Disks[0].ReserveBytes != 5<<30 ||
		got.VMs != (protocol.ServerCapacityVMs{Awake: 3, Frozen: 1, Paused: 2, Saved: 4}) {
		t.Fatalf("machine = %+v", got)
	}
	if last := got.LastLaunchRefusal; last == nil || last.RunID != "run-new" || last.Op != "launch-retry" || last.Text != "scheduler: insufficient host memory headroom: 1 available bytes" {
		t.Fatalf("last launch refusal = %+v, want the latest, a retry, with the refusal as its caller got it", last)
	}
	if last := got.LastWakeRefusal; last == nil || last.RunID != "run-asleep" || last.Cause != "disk" {
		t.Fatalf("last wake refusal = %+v", last)
	}
	count := func(counts []protocol.ServerCapacityCount, kind, op, cause string) protocol.ServerCapacityCount {
		for _, c := range counts {
			if c.Kind == kind && c.Op == op && c.Cause == cause {
				return c
			}
		}
		return protocol.ServerCapacityCount{}
	}
	if len(got.Day) != 3 || len(got.Fortnight) != 4 {
		t.Fatalf("counters: %d in the day, %d in the fortnight; want 3 and 4, without the save 20 days ago", len(got.Day), len(got.Fortnight))
	}
	if c := count(got.Fortnight, "refusal", "launch", "memory"); c.Count != 1 || c.IdleHourBytes != 20 || count(got.Day, "refusal", "launch", "memory").Count != 0 {
		t.Fatalf("the refusal ten days ago = %+v, want it in the fortnight only", c)
	}
	if c := count(got.Day, "wait-end", "subsession", "started"); c.WaitedSeconds != 90 || c.MaxWaitedSeconds != 90 {
		t.Fatalf("waits that ended = %+v", c)
	}
	// Whose session was refused is the admin's to see.
	own := read(member.ID)
	if own.LastLaunchRefusal == nil || own.LastLaunchRefusal.RunID != "" || own.LastWakeRefusal.RunID != "" || own.LastLaunchRefusal.Text == "" {
		t.Fatalf("a member's view of the refusals = %+v, %+v; want the text without the session", own.LastLaunchRefusal, own.LastWakeRefusal)
	}
}
