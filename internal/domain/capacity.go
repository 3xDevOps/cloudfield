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

// VMCounts are the session VMs by what they hold of the host. Of open
// sessions: awake (one a member paused too), awake and frozen by the memory
// brake, asleep and still running as a server restart left it, asleep and
// saved to disk, and asleep with neither: stopped, or still being saved. Closed are the VMs of closed
// sessions that can still hold memory.
type VMCounts struct {
	Awake   int
	Frozen  int
	Paused  int
	Saved   int
	Stopped int
	Closed  int
}

// SumCapacityCounts adds the hours of each counter together, in the order
// each counter first appears.
func SumCapacityCounts(hours []CapacityCount) []CapacityCount {
	var out []CapacityCount
	at := map[[3]string]int{}
	for _, h := range hours {
		key := [3]string{h.Kind, h.Op, h.Cause}
		i, ok := at[key]
		if !ok {
			i = len(out)
			at[key] = i
			out = append(out, CapacityCount{Kind: h.Kind, Op: h.Op, Cause: h.Cause})
		}
		c := &out[i]
		c.Count += h.Count
		c.AvailableBytes += h.AvailableBytes
		c.FloorBytes += h.FloorBytes
		c.UsedBytes += h.UsedBytes
		c.IdleHourBytes += h.IdleHourBytes
		c.VMBytes += h.VMBytes
		c.Waited += h.Waited
		c.MaxWaited = max(c.MaxWaited, h.MaxWaited)
		if !h.LastAt.Before(c.LastAt) {
			c.LastAt, c.LastRun, c.LastText = h.LastAt, h.LastRun, h.LastText
		}
	}
	return out
}

// LastCapacityRefusal is the latest refusal among counts of an admission
// whose op is one of ops, nil when there is none.
func LastCapacityRefusal(counts []CapacityCount, ops ...string) *CapacityCount {
	var last *CapacityCount
	for i, c := range counts {
		if c.Kind != CapacityRefusal || (last != nil && !c.LastAt.After(last.LastAt)) {
			continue
		}
		for _, op := range ops {
			if c.Op == op {
				last = &counts[i]
			}
		}
	}
	return last
}
