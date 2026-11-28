package protocol

// MethodServerCapacity reads what the server measures before it starts or
// wakes a session, its session VMs, and what it did for want of memory or
// disk.
const MethodServerCapacity = "server.capacity"

// ServerCapacityResult is the one machine the server runs on. A measurement
// that failed leaves its figures zero and its error says why.
type ServerCapacityResult struct {
	MemoryTotalBytes     uint64 `json:"memory_total_bytes"`
	MemoryAvailableBytes uint64 `json:"memory_available_bytes"`
	// MemoryFloorBytes is the available memory the server keeps: under it
	// a launch is refused and the session VMs are frozen and saved to
	// disk. A launch needs at least StartMemoryBytes above it, more while
	// other starts have yet to take theirs; the wake of a VM saved to disk
	// needs what it saved.
	MemoryFloorBytes uint64 `json:"memory_floor_bytes"`
	StartMemoryBytes uint64 `json:"start_memory_bytes"`
	MemoryError      string `json:"memory_error,omitempty"`
	// ServerStall is the percentage of the last 60 seconds in which the
	// server's own cgroup stalled on memory; launches are refused at
	// ServerStallLimit. HostStall is that of the last 10 seconds for every
	// task on the host; VMs are saved once it stays at HostStallLimit.
	// Either is absent where the kernel does not report it.
	ServerStall      *float64 `json:"server_stall,omitempty"`
	ServerStallLimit float64  `json:"server_stall_limit"`
	HostStall        *float64 `json:"host_stall,omitempty"`
	HostStallLimit   float64  `json:"host_stall_limit"`
	StallError       string   `json:"stall_error,omitempty"`
	// Disks are the filesystems a launch needs at least StartDiskBytes
	// above the reserve of. DiskUnchecked says the server was started with
	// the disk check off: Disks is then empty and no launch is refused for
	// disk.
	Disks          []ServerCapacityDisk `json:"disks"`
	StartDiskBytes uint64               `json:"start_disk_bytes"`
	DiskError      string               `json:"disk_error,omitempty"`
	DiskUnchecked  bool                 `json:"disk_unchecked,omitempty"`
	VMs            ServerCapacityVMs    `json:"vms"`
	// LastLaunchRefusal and LastWakeRefusal are the latest capacity
	// refusal of each in the last 14 days, with admission's text. Op tells
	// a wake nobody asked for ("wake-auto") from one somebody did.
	LastLaunchRefusal *ServerCapacityRefusal `json:"last_launch_refusal,omitempty"`
	LastWakeRefusal   *ServerCapacityRefusal `json:"last_wake_refusal,omitempty"`
	// Day and Fortnight are the capacity counters summed over the hours
	// (UTC, whole) of the last 24 hours and the last 14 days.
	Day       []ServerCapacityCount `json:"day"`
	Fortnight []ServerCapacityCount `json:"fortnight"`
}

type ServerCapacityDisk struct {
	Name         string `json:"name"`
	TotalBytes   uint64 `json:"total_bytes"`
	FreeBytes    uint64 `json:"free_bytes"`
	ReserveBytes uint64 `json:"reserve_bytes"`
}

// ServerCapacityVMs counts the session VMs. Of open sessions: awake (one a
// member paused too), awake and frozen by the memory brake, asleep and
// still running as a server restart left it, asleep and saved to disk, and
// asleep with neither (stopped, or still being saved). Closed are the VMs of closed sessions
// that can still hold memory.
type ServerCapacityVMs struct {
	Awake   int `json:"awake"`
	Frozen  int `json:"frozen"`
	Paused  int `json:"paused"`
	Saved   int `json:"saved"`
	Stopped int `json:"stopped"`
	Closed  int `json:"closed"`
}

type ServerCapacityRefusal struct {
	At    string `json:"at"`
	Op    string `json:"op"`
	Cause string `json:"cause"`
	// Text is admission's refusal. For a caller who is not a super admin
	// one that names a failed measurement is replaced by a fixed sentence:
	// such a refusal can carry a path of the host.
	Text string `json:"text"`
	// RunID is the session refused, for a super admin.
	RunID string `json:"run_id,omitempty"`
}

// ServerCapacityCount is one counter: Kind is refusal, wait, wait-end,
// freeze or save. The byte fields are sums over its Count events.
type ServerCapacityCount struct {
	Kind             string `json:"kind"`
	Op               string `json:"op,omitempty"`
	Cause            string `json:"cause,omitempty"`
	Count            int64  `json:"count"`
	AvailableBytes   uint64 `json:"available_bytes,omitempty"`
	FloorBytes       uint64 `json:"floor_bytes,omitempty"`
	UsedBytes        uint64 `json:"used_bytes,omitempty"`
	IdleHourBytes    uint64 `json:"idle_hour_bytes,omitempty"`
	VMBytes          uint64 `json:"vm_bytes,omitempty"`
	WaitedSeconds    int64  `json:"waited_seconds,omitempty"`
	MaxWaitedSeconds int64  `json:"max_waited_seconds,omitempty"`
	LastAt           string `json:"last_at"`
}
