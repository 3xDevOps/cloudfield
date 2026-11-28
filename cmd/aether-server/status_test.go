package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func TestStatusPrintsTheMachineTheRefusalsAndTheThreeAnswers(t *testing.T) {
	stall := 12.5
	r := protocol.ServerCapacityResult{
		MemoryTotalBytes: 32 << 30, MemoryAvailableBytes: 1 << 30, MemoryFloorBytes: 3 << 30, StartMemoryBytes: 512 << 20,
		ServerStall: &stall, ServerStallLimit: 10, HostStallLimit: 60,
		Disks:           []protocol.ServerCapacityDisk{{Name: "Cloudfield data filesystem", TotalBytes: 100 << 30, FreeBytes: 4 << 30, ReserveBytes: 5 << 30}},
		StartDiskBytes:  1 << 30,
		VMs:             protocol.ServerCapacityVMs{Awake: 3, Frozen: 1, Paused: 2, Saved: 4, Stopped: 5},
		LastWakeRefusal: &protocol.ServerCapacityRefusal{At: "2026-11-27T22:11:00Z", Op: "wake-auto", Cause: "disk", Text: "scheduler: insufficient disk headroom"},
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
	printStatus(&out, r, time.Date(2026, 11, 27, 23, 0, 0, 0, time.UTC), time.Date(2026, 11, 27, 23, 0, 40, 0, time.UTC), nil)
	for _, want := range []string{
		"memory   1.0 GiB available of 32.0 GiB; the server keeps 3.0 GiB, and a launch needs at least 0.5 GiB above that",
		"which refuses launches: 12.5%, limit 10%",
		"which saves session VMs once it lasts 30s: not measured (the kernel reports no memory pressure)",
		"disk     Cloudfield data filesystem: 4.0 GiB free of 100.0 GiB; the server keeps 5.0 GiB, and a launch needs at least 1.0 GiB above that",
		"VMs      3 awake, 1 frozen by the memory brake, 2 asleep in memory, 4 asleep on disk, 5 asleep with nothing kept",
		"counted by the server 40s ago, at 2026-11-27T23:00:00Z",
		"last refused launch: 2026-11-27T22:10:03Z, session run-new\n  scheduler: insufficient host memory headroom: 1 available bytes",
		"last refused wake: 2026-11-27T22:11:00Z, a wake the server tried by itself\n  scheduler: insufficient disk headroom",
		"refusal launch memory                       2          5",
		"refusal wake memory                         0          7",
		"or closed, of the memory in use at memory refusals        25%        60%",
		"waits that ended in a start, 24 hours: 1m0s mean, 1m30s longest",
		"waits that ended in a start, 14 days:  1m40s mean, 5m0s longest",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
}

// With no database to read, the command still prints what it measured and
// says the history is unknown; and it never creates or changes a database.
func TestStatusPrintsItsMeasurementsWithoutADatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aether.db")
	_, _, _, err := statusHistory(t.Context(), path, time.Now())
	if err == nil {
		t.Fatal("a missing database read without an error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("reading a missing database left %v behind, want nothing created", entries)
	}
	var out strings.Builder
	printStatus(&out, protocol.ServerCapacityResult{MemoryTotalBytes: 8 << 30, MemoryAvailableBytes: 7 << 30, MemoryFloorBytes: 2 << 30, StartMemoryBytes: 512 << 20, DiskUnchecked: true},
		time.Time{}, time.Now(), err)
	for _, want := range []string{
		"memory   7.0 GiB available of 8.0 GiB",
		"disk     not checked: --min-free-disk is negative, so no launch is refused for disk",
		"history  not read, so the session VMs, the refusals and the counts below are unknown: store: open " + path,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "none in 14 days") || strings.Contains(out.String(), "nothing counted") {
		t.Errorf("status with no history claims nothing happened:\n%s", out.String())
	}

	// A database an older server made is read as it is, never migrated.
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, counted, hours, err := statusHistory(t.Context(), path, time.Now()); err != nil || !counted.IsZero() || len(hours) != 0 {
		t.Fatalf("history of a database with nothing recorded = %v, %v, %v", counted, hours, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reading the history changed the database file (%v)", err)
	}
}
