package vm

// SweepImages deletes the base and helper disks that no image tag and no VM
// uses any more.
func (r *Runtime) SweepImages() error { return r.vms.cfg.Images.Sweep() }

// DropKeptCheckoutDisks deletes the prepared checkout disks kept for the
// next launch that no session uses.
func (r *Runtime) DropKeptCheckoutDisks() error { return r.vms.cfg.Images.DropKeptCheckoutDisks() }
