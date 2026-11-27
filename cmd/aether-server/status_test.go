package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestStatusPrintsTheMachineTheRefusalsAndTheThreeAnswers(t *testing.T) {
	stall := 12.5
	r := protocol.ServerCapacityResult{
		MemoryTotalBytes: 32 << 30, MemoryAvailableBytes: 1 << 30, MemoryFloorBytes: 3 << 30, StartMemoryBytes: 512 << 20,
		ServerStall: &stall, ServerStallLimit: 10, HostStallLimit: 60,
		Disks:          []protocol.ServerCapacityDisk{{Name: "Cloudfield data filesystem", TotalBytes: 100 << 30, FreeBytes: 4 << 30, ReserveBytes: 5 << 30}},
		StartDiskBytes: 1 << 30,
		VMs:            protocol.ServerCapacityVMs{Awake: 3, Frozen: 1, Paused: 2, Saved: 4},
		LastLaunchRefusal: &protocol.ServerCapacityRefusal{At: "2026-11-27T22:10:03Z", Op: "launch", Cause: "memory", RunID: "run-new",
			Text: "scheduler: insufficient host memory headroom: 1 available bytes"},
		Day: []protocol.ServerCapacityCount{
			{Kind: "refusal", Op: "launch", Cause: "memory", Count: 2, UsedBytes: 200, IdleHourBytes: 50},
			{Kind: "wait-end", Op: "subsession", Cause: "started", Count: 2, WaitedSeconds: 120, MaxWaitedSeconds: 90},
		},
		Fortnight: []protocol.ServerCapacityCount{
			{Kind: "refusal", Op: "launch", Cause: "memory", Count: 5, UsedBytes: 500, IdleHourBytes: 300},
			{Kind: "refusal", Op: "wake", Cause: "memory", Count: 7, UsedBytes: 500, IdleHourBytes: 300},
			{Kind: "wait-end", Op: "subsession", Cause: "started", Count: 4, WaitedSeconds: 400, MaxWaitedSeconds: 300},
		},
	}
	var out strings.Builder
	printStatus(&out, r, time.Date(2026, 11, 27, 23, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"memory   1.0 GiB available of 32.0 GiB; the server keeps 3.0 GiB, and a launch needs 0.5 GiB above that",
		"which refuses launches: 12.5%, limit 10%",
		"which saves session VMs: not measured (the kernel reports no memory pressure)",
		"disk     Cloudfield data filesystem: 4.0 GiB free of 100.0 GiB; the server keeps 5.0 GiB, and a launch needs 1.0 GiB above that",
		"VMs      3 awake, 1 frozen by the memory brake, 2 asleep in memory, 4 asleep on disk (counted 2026-11-27T23:00:00Z)",
		"last refused launch: 2026-11-27T22:10:03Z session run-new\n  scheduler: insufficient host memory headroom: 1 available bytes",
		"last refused wake: none in 14 days",
		"refusal launch memory                       2          5",
		"refusal wake memory                         0          7",
		"of the memory in use at memory refusals        25%        60%",
		"waits that ended in a start, 24 hours: 1m0s mean, 1m30s longest",
		"waits that ended in a start, 14 days:  1m40s mean, 5m0s longest",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
}
