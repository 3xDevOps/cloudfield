package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
)

func init() {
	registerMethod(protocol.MethodServerCapacity, (*Server).serverCapacity)
}

func (s *Server) serverCapacity(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	now := time.Now()
	hours, err := s.cfg.Store.CapacityCounts(ctx, now.Add(-CapacityFortnight))
	if err != nil {
		return nil, rpcError(err)
	}
	result := CapacityResult(s.cfg.Runs.Machine(ctx), s.cfg.Runs.VMs(), hours, now)
	if s.superAdmin(ctx, member) {
		return result, nil
	}
	// A failed measurement's error, and a refusal that quotes one, can
	// name a path of the host, as server.disk's does.
	for _, e := range []*string{&result.MemoryError, &result.StallError, &result.DiskError} {
		if *e != "" {
			*e = unmeasured
		}
	}
	for _, r := range []*protocol.ServerCapacityRefusal{result.LastLaunchRefusal, result.LastWakeRefusal} {
		if r == nil {
			continue
		}
		r.RunID = ""
		if r.Cause == "unknown" {
			r.Text = "the server could not measure its memory or disk; a super admin sees its error"
		}
	}
	return result, nil
}

const unmeasured = "not measured; a super admin sees the server's error"

// CapacityFortnight is how far back server.capacity counts.
const CapacityFortnight = 14 * 24 * time.Hour

// CapacityResult puts a measured machine, its VM counts and the counters of
// the hours since now minus CapacityFortnight into the wire form. The sums
// cover whole hours; a last refusal is within the fortnight to the second.
func CapacityResult(m scheduler.MachineStatus, vms domain.VMCounts, hours []domain.CapacityCount, now time.Time) protocol.ServerCapacityResult {
	result := protocol.ServerCapacityResult{
		MemoryTotalBytes: m.MemoryTotalBytes, MemoryAvailableBytes: m.MemoryAvailableBytes, MemoryFloorBytes: m.MemoryFloorBytes,
		StartMemoryBytes: m.StartMemoryBytes, MemoryError: m.MemoryError,
		ServerStall: m.ServerStall, ServerStallLimit: m.ServerStallLimit, HostStall: m.HostStall, HostStallLimit: m.HostStallLimit, StallError: m.StallError,
		Disks: []protocol.ServerCapacityDisk{}, StartDiskBytes: m.StartDiskBytes, DiskError: m.DiskError, DiskUnchecked: m.DiskUnchecked,
		VMs: protocol.ServerCapacityVMs{Awake: vms.Awake, Frozen: vms.Frozen, Paused: vms.Paused, Saved: vms.Saved, Stopped: vms.Stopped},
	}
	for _, f := range m.Filesystems {
		result.Disks = append(result.Disks, protocol.ServerCapacityDisk{Name: f.Name, TotalBytes: f.TotalBytes, FreeBytes: f.FreeBytes, ReserveBytes: f.ReserveBytes})
	}
	day := now.Add(-24 * time.Hour).UTC().Truncate(time.Hour)
	var recent []domain.CapacityCount
	for _, h := range hours {
		if !h.Hour.Before(day) {
			recent = append(recent, h)
		}
	}
	fortnight := domain.SumCapacityCounts(hours)
	result.Day, result.Fortnight = wireCapacityCounts(domain.SumCapacityCounts(recent)), wireCapacityCounts(fortnight)
	since := now.Add(-CapacityFortnight)
	result.LastLaunchRefusal = wireCapacityRefusal(domain.LastCapacityRefusal(fortnight, "launch", "launch-retry"), since)
	result.LastWakeRefusal = wireCapacityRefusal(domain.LastCapacityRefusal(fortnight, "wake", "wake-auto"), since)
	return result
}

func wireCapacityCounts(counts []domain.CapacityCount) []protocol.ServerCapacityCount {
	out := make([]protocol.ServerCapacityCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, protocol.ServerCapacityCount{
			Kind: c.Kind, Op: c.Op, Cause: c.Cause, Count: c.Count,
			AvailableBytes: c.AvailableBytes, FloorBytes: c.FloorBytes, UsedBytes: c.UsedBytes, IdleHourBytes: c.IdleHourBytes, VMBytes: c.VMBytes,
			WaitedSeconds: int64(c.Waited / time.Second), MaxWaitedSeconds: int64(c.MaxWaited / time.Second),
			LastAt: c.LastAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func wireCapacityRefusal(c *domain.CapacityCount, since time.Time) *protocol.ServerCapacityRefusal {
	if c == nil || c.LastAt.Before(since) {
		return nil
	}
	return &protocol.ServerCapacityRefusal{At: c.LastAt.UTC().Format(time.RFC3339), Op: c.Op, Cause: c.Cause, Text: c.LastText, RunID: string(c.LastRun)}
}
