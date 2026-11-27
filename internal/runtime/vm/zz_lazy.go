package vm

// Experiment switches for scratch/lazy-ch-wake-speed-and-memory. Not a
// product change: a kvm test sets them, nothing else does.
var (
	// zzPrivate boots a VM without shares on private guest memory.
	zzPrivate bool
	// zzRestoreMode is appended to the --restore argument.
	zzRestoreMode string
	// zzKeep leaves the memory file a restore used in place while the VM
	// runs; the next save writes beside it and replaces it once the
	// monitor has stopped.
	zzKeep bool
	// zzHypervisor replaces the cloud-hypervisor binary the monitor runs.
	zzHypervisor string
	// zzMark is called as a restore passes each step.
	zzMark = func(string) {}
	// zzPaused runs while a restored VM is still paused.
	zzPaused = func(string) {}
)

func zzShared(shares int) string {
	if zzPrivate && shares == 0 {
		return "off"
	}
	return "on"
}
