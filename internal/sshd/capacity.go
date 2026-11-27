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

// MachineReader is the scheduler measuring its machine and counting its
// session VMs.
type MachineReader interface {
	Machine(ctx context.Context) scheduler.MachineStatus
	VMs() domain.VMCounts
}

func (s *Server) serverCapacity(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	reader, ok := s.cfg.Runs.(MachineReader)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "server.capacity: this server does not measure its machine"}
	}
	now := time.Now()
	hours, err := s.cfg.Store.CapacityCounts(ctx, now.Add(-CapacityFortnight))
	if err != nil {
		return nil, rpcError(err)
	}
	result := CapacityResult(reader.Machine(ctx), reader.VMs(), hours, now)
	if !s.superAdmin(ctx, member) {
		for _, r := range []*protocol.ServerCapacityRefusal{result.LastLaunchRefusal, result.LastWakeRefusal} {
			if r != nil {
				r.RunID = ""
			}
		}
	}
	return result, nil
}

// CapacityFortnight is how far back server.capacity counts.
const CapacityFortnight = 14 * 24 * time.Hour

// CapacityResult puts a measured machine, its VM counts and the counters of
// the hours since now minus CapacityFortnight into the wire form.
func CapacityResult(m scheduler.MachineStatus, vms domain.VMCounts, hours []domain.CapacityCount, now time.Time) protocol.ServerCapacityResult {
	result := protocol.ServerCapacityResult{
		MemoryTotalBytes: m.MemoryTotalBytes, MemoryAvailableBytes: m.MemoryAvailableBytes, MemoryFloorBytes: m.MemoryFloorBytes,
		StartMemoryBytes: m.StartMemoryBytes, MemoryError: m.MemoryError,
		ServerStall: m.ServerStall, ServerStallLimit: m.ServerStallLimit, HostStall: m.HostStall, HostStallLimit: m.HostStallLimit, StallError: m.StallError,
		Disks: []protocol.ServerCapacityDisk{}, StartDiskBytes: m.StartDiskBytes, DiskError: m.DiskError,
		VMs: protocol.ServerCapacityVMs{Awake: vms.Awake, Frozen: vms.Frozen, Paused: vms.Paused, Saved: vms.Saved},
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
	result.LastLaunchRefusal = wireCapacityRefusal(domain.LastCapacityRefusal(fortnight, "launch", "launch-retry"))
	result.LastWakeRefusal = wireCapacityRefusal(domain.LastCapacityRefusal(fortnight, "wake"))
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

func wireCapacityRefusal(c *domain.CapacityCount) *protocol.ServerCapacityRefusal {
	if c == nil {
		return nil
	}
	return &protocol.ServerCapacityRefusal{At: c.LastAt.UTC().Format(time.RFC3339), Op: c.Op, Cause: c.Cause, Text: c.LastText, RunID: string(c.LastRun)}
}
