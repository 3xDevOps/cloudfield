package vm

import "time"

// lazy holds the measurement hooks of the scratch/lazy-* experiments. It is
// never merged: with every field unset the engine behaves as on main.
var lazy struct {
	// shared is the --memory shared= value of a boot; empty means on.
	shared string
	// restoreMode is appended to --restore, as
	// ",memory_restore_mode=copyonwrite".
	restoreMode string
	// reveal is one more host directory a VM's unit may write.
	reveal string
	// unitExtraMiB is added to the MemoryMax of a VM's unit.
	unitExtraMiB int64

	beforePause   func(key string)
	afterSnapshot func(key, dir string) error
	afterSync     func(key, dir string, api chAPI)

	sync1, report, sync2, pause, snapshot, hook, fsync time.Duration
}
