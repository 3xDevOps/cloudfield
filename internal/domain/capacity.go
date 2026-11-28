package domain

import "time"

// Kinds of CapacityEvent.
const (
	// CapacityRefusal is an admission that refused a launch, wake or boot.
	CapacityRefusal = "refusal"
	// CapacityWait is a launch that began to wait for room, and
	// CapacityWaitEnd one that stopped waiting: Cause says how.
	CapacityWait    = "wait"
	CapacityWaitEnd = "wait-end"
	// CapacityFreeze is the memory brake freezing the session VMs.
	CapacityFreeze = "freeze"
	// CapacitySave is the memory guard, or an admission short of memory,
	// saving a session VM to disk.
	CapacitySave = "save"
)

// CapacityEvent is one thing the server did because it was short of memory
// or disk. Kind, Op and Cause name its counter; the figures are added to
// that counter's sums, and Run and Text become its last event.
type CapacityEvent struct {
	At    time.Time
	Kind  string
	Op    string
	Cause string
	Run   RunID
	// Text is the refusal as its caller got it, or what was done.
	Text string
	// AvailableBytes and FloorBytes are the host's available memory and
	// the floor admission keeps of it.
	AvailableBytes uint64
	FloorBytes     uint64
	// UsedBytes is the host memory in use at a memory refusal, and
	// IdleHourBytes what session VMs idle for over an hour held of it.
	UsedBytes     uint64
	IdleHourBytes uint64
	// VMBytes is the host memory a saved VM held.
	VMBytes uint64
	// Waited is how long a launch that stopped waiting had waited.
	Waited time.Duration
}

// CapacityCount is the events of one counter within one hour.
type CapacityCount struct {
	Hour  time.Time
	Kind  string
	Op    string
	Cause string
	Count int64
	// The sums of each event's figure.
	AvailableBytes uint64
	FloorBytes     uint64
	UsedBytes      uint64
	IdleHourBytes  uint64
	VMBytes        uint64
	Waited         time.Duration
	// MaxWaited is the longest single wait.
	MaxWaited time.Duration
	LastAt    time.Time
	LastRun   RunID
	LastText  string
}
