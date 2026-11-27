package store

import (
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func TestCapacityEventsAddUpPerHourAndCounter(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	hour := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	for _, e := range []domain.CapacityEvent{
		{At: hour.Add(time.Minute), Kind: domain.CapacityRefusal, Op: "launch", Cause: "memory", Run: "run-a", Text: "first",
			AvailableBytes: 1, FloorBytes: 10, UsedBytes: 100, IdleHourBytes: 40},
		{At: hour.Add(2 * time.Minute), Kind: domain.CapacityRefusal, Op: "launch", Cause: "memory", Run: "run-b", Text: "second",
			AvailableBytes: 2, FloorBytes: 10, UsedBytes: 100, IdleHourBytes: 60},
		{At: hour.Add(3 * time.Minute), Kind: domain.CapacityRefusal, Op: "wake", Cause: "memory", Run: "run-c", Text: "a wake"},
		{At: hour.Add(time.Hour), Kind: domain.CapacityWaitEnd, Op: "subsession", Cause: "started", Waited: 90 * time.Second},
		{At: hour.Add(time.Hour + time.Minute), Kind: domain.CapacityWaitEnd, Op: "subsession", Cause: "started", Waited: 30 * time.Second},
		{At: hour.Add(-time.Hour), Kind: domain.CapacitySave, Op: "idle", VMBytes: 7},
	} {
		if err := db.CountCapacityEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.CapacityCounts(ctx, hour.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.CapacityCount{
		{Hour: hour, Kind: domain.CapacityRefusal, Op: "launch", Cause: "memory", Count: 2, AvailableBytes: 3, FloorBytes: 20, UsedBytes: 200,
			IdleHourBytes: 100, LastAt: hour.Add(2 * time.Minute), LastRun: "run-b", LastText: "second"},
		{Hour: hour, Kind: domain.CapacityRefusal, Op: "wake", Cause: "memory", Count: 1, LastAt: hour.Add(3 * time.Minute), LastRun: "run-c", LastText: "a wake"},
		{Hour: hour.Add(time.Hour), Kind: domain.CapacityWaitEnd, Op: "subsession", Cause: "started", Count: 2, Waited: 2 * time.Minute,
			MaxWaited: 90 * time.Second, LastAt: hour.Add(time.Hour + time.Minute)},
	}
	if len(got) != len(want) {
		t.Fatalf("counts from the hour on = %+v, want %d rows without the hour before", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("count %d = %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestVMCountsKeepTheLatest(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	if _, at, err := db.VMCounts(ctx); err != nil || !at.IsZero() {
		t.Fatalf("counts before any = %v, %v; want none", at, err)
	}
	first, second := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC), time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	if err := db.RecordVMCounts(ctx, domain.VMCounts{Awake: 9}, first); err != nil {
		t.Fatal(err)
	}
	want := domain.VMCounts{Awake: 1, Frozen: 2, Paused: 3, Saved: 4}
	if err := db.RecordVMCounts(ctx, want, second); err != nil {
		t.Fatal(err)
	}
	if got, at, err := db.VMCounts(ctx); err != nil || got != want || !at.Equal(second) {
		t.Fatalf("counts = %+v at %v, %v; want the latest", got, at, err)
	}
}
