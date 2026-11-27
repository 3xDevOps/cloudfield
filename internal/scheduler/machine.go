package scheduler

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/3xDevOps/cloudfield/internal/disk"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/sysstats"
)

// MachineStatus is the machine as admission and the memory and disk guards
// measure it. A measurement that failed leaves its figures zero and its
// error says why.
type MachineStatus struct {
	MemoryTotalBytes     uint64
	MemoryAvailableBytes uint64
	// MemoryFloorBytes is the available memory admission keeps. Under it
	// the session VMs are frozen and saved to disk.
	MemoryFloorBytes uint64
	// StartMemoryBytes is what a launch needs above the floor.
	StartMemoryBytes uint64
	MemoryError      string
	// ServerStall is the percentage of the last 60 seconds in which the
	// server's own cgroup stalled on memory, nil when it has no cgroup of
	// its own that reports it. Launches are refused at ServerStallLimit.
	ServerStall      *float64
	ServerStallLimit float64
	// HostStall is the percentage of the last 10 seconds in which the whole
	// host stalled on memory, nil when the kernel does not report it. The
	// memory guard saves VMs once it has stayed at HostStallLimit.
	HostStall      *float64
	HostStallLimit float64
	StallError     string
	Filesystems    []MachineFilesystem
	// StartDiskBytes is what a launch needs above a filesystem's reserve.
	StartDiskBytes uint64
	DiskError      string
}

// MachineFilesystem is a filesystem admission checks and the free space it
// keeps on it.
type MachineFilesystem struct {
	Name         string
	TotalBytes   uint64
	FreeBytes    uint64
	ReserveBytes uint64
}

// serverMemoryPressure is the "some avg60" memory pressure of the cgroup of
// the server running as pid. It is nil on a host without cgroup v2 pressure
// accounting and for a server in the root cgroup, whose file would be
// host-wide.
func serverMemoryPressure(pid int) (*float64, error) {
	cgroup, err := cgroupPath(procRoot, pid)
	if err == nil && cgroup != "/" {
		var pressure *float64
		if pressure, err = sysstats.CgroupMemoryPressure(filepath.Join(cgroupRoot, cgroup)); err == nil {
			return pressure, nil
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return nil, err
}

// MeasureMachine measures the machine for the server running as serverPID
// with its data directory and --min-free-disk, without asking that server:
// it answers while the server cannot.
func MeasureMachine(serverPID int, dataDir string, minFree int64) MachineStatus {
	m := MachineStatus{StartMemoryBytes: startupMemoryBytes, ServerStallLimit: maxMemoryPressure, HostStallLimit: memoryStallLimit, StartDiskBytes: startupDiskBytes}
	total, available, err := sysstats.HostMemory(procRoot)
	m.setMemory(total, available, err)
	var stallErr error
	if serverPID != 0 {
		m.ServerStall, stallErr = serverMemoryPressure(serverPID)
	}
	host, hostErr := sysstats.HostMemoryStall(procRoot)
	m.HostStall = host
	if err = errors.Join(stallErr, hostErr); err != nil {
		m.StallError = err.Error()
	}
	if minFree >= 0 {
		data, diskErr := disk.Filesystem(dataDir)
		if diskErr != nil {
			m.DiskError = diskErr.Error()
		} else {
			m.setFilesystems([]runtime.FilesystemCapacity{{Name: "Cloudfield data filesystem", TotalBytes: data.TotalBytes, FreeBytes: data.FreeBytes}}, minFree)
		}
	}
	return m
}

func (m *MachineStatus) setMemory(total, available uint64, err error) {
	if err != nil {
		m.MemoryError = err.Error()
		return
	}
	m.MemoryTotalBytes, m.MemoryAvailableBytes, m.MemoryFloorBytes = total, available, memoryFloor(total)
}

func (m *MachineStatus) setFilesystems(filesystems []runtime.FilesystemCapacity, minFree int64) {
	m.Filesystems = nil
	for _, f := range filesystems {
		m.Filesystems = append(m.Filesystems, MachineFilesystem{Name: f.Name, TotalBytes: f.TotalBytes, FreeBytes: f.FreeBytes, ReserveBytes: disk.Reserve(f.TotalBytes, minFree)})
	}
}

// Machine is MeasureMachine by the running server, with the filesystems of
// its runtime and the memory its admission reads.
func (s *Scheduler) Machine(ctx context.Context) MachineStatus {
	m := MeasureMachine(os.Getpid(), "", -1)
	total, available, err := s.hostMemoryTotal()
	m.MemoryError = ""
	m.setMemory(total, available, err)
	probeCtx, cancel := context.WithTimeout(ctx, capacityProbeTimeout)
	defer cancel()
	filesystems, _, err := s.measureCapacity(probeCtx)
	if err != nil {
		m.DiskError = err.Error()
	} else {
		m.setFilesystems(filesystems, s.cfg.MinFreeBytes)
	}
	return m
}

// VMs counts the session VMs by what they hold of the host: awake and
// running, awake and frozen by the memory brake, asleep and paused in
// memory, asleep and saved to disk.
func (s *Scheduler) VMs() domain.VMCounts {
	var frozen []runtime.ID
	if vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime); ok {
		frozen = vms.Frozen()
	}
	var c domain.VMCounts
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.runs {
		if _, vm := s.saver(e.containerID); !vm || e.retained {
			continue
		}
		switch {
		case e.asleepSince.IsZero() && slices.Contains(frozen, e.containerID):
			c.Frozen++
		case e.asleepSince.IsZero():
			c.Awake++
		case e.asleepResident:
			c.Paused++
		default:
			c.Saved++
		}
	}
	return c
}
