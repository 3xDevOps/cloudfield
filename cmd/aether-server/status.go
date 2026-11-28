package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
	"github.com/3xDevOps/cloudfield/internal/server"
	"github.com/3xDevOps/cloudfield/internal/sshd"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const statusUsage = "usage: cloudfield-server status [--config <file>]"

func statusCmd(args []string) error {
	o, configPath, rest, err := loadOptions("status", args, nil)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(statusUsage)
	}
	// No pid when the unit is not running: the server's own stall is then
	// left out.
	pid, _ := strconv.Atoi(unitPID())
	now := time.Now()
	machine := scheduler.MeasureMachine(pid, *o.dataDir, *o.minFreeDisk)
	vms, counted, hours, historyErr := statusHistory(context.Background(), server.StorePath(*o.dataDir), now)
	printStatus(os.Stdout, sshd.CapacityResult(machine, vms, hours, now), counted, now, historyErr)
	_, _ = fmt.Fprintf(os.Stdout, "\nMeasured by this command with the options in %s: a server started before they changed still runs with its own.\n", configPath)
	return nil
}

// statusHistory reads what the server recorded, from a database it only
// reads: a server of another version may be using it.
func statusHistory(ctx context.Context, path string, now time.Time) (domain.VMCounts, time.Time, []domain.CapacityCount, error) {
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return domain.VMCounts{}, time.Time{}, nil, err
	}
	defer db.Close() //nolint:errcheck // opened for reading
	vms, counted, err := db.VMCounts(ctx)
	if err != nil {
		return domain.VMCounts{}, time.Time{}, nil, err
	}
	hours, err := db.CapacityCounts(ctx, now.Add(-sshd.CapacityFortnight))
	return vms, counted, hours, err
}

// printStatus prints r as measured at now. counted is when the server last
// wrote its session VM counts, zero when it never did, and historyErr why
// the database could not be read, when it could not.
func printStatus(w io.Writer, r protocol.ServerCapacityResult, counted, now time.Time, historyErr error) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	if r.MemoryError != "" {
		p("memory   not measured: %s", r.MemoryError)
	} else {
		p("memory   %s available of %s; the server keeps %s, and a launch needs at least %s above that", gib(r.MemoryAvailableBytes), gib(r.MemoryTotalBytes), gib(r.MemoryFloorBytes), gib(r.StartMemoryBytes))
		p("         (more while other starts have yet to take theirs; the wake of a VM saved to disk needs what it saved)")
	}
	stall := func(v *float64, limit float64, what, missing string) {
		switch {
		case v != nil:
			p("stall    %s: %.1f%%, limit %.0f%%", what, *v, limit)
		case r.StallError == "":
			p("stall    %s: not measured (%s)", what, missing)
		default:
			p("stall    %s: not measured", what)
		}
	}
	stall(r.ServerStall, r.ServerStallLimit, "the running "+updateUnit+" unit's cgroup over 60s, which refuses launches", "no such unit is running with a cgroup of its own that reports memory.pressure")
	stall(r.HostStall, r.HostStallLimit, "the host over 10s, which saves session VMs once it lasts 30s", "the kernel reports no memory pressure")
	if r.StallError != "" {
		p("stall    not measured: %s", r.StallError)
	}
	switch {
	case r.DiskUnchecked:
		p("disk     not checked: --min-free-disk is negative, so no launch is refused for disk")
	case r.DiskError != "":
		p("disk     not measured: %s", r.DiskError)
	}
	for _, d := range r.Disks {
		p("disk     %s: %s free of %s; the server keeps %s, and a launch needs at least %s above that", d.Name, gib(d.FreeBytes), gib(d.TotalBytes), gib(d.ReserveBytes), gib(r.StartDiskBytes))
	}
	if !r.DiskUnchecked {
		p("         (the running server also checks the filesystems of its container or VM runtime, which this command does not measure)")
	}
	if historyErr != nil {
		p("history  not read, so the session VMs, the refusals and the counts below are unknown: %v", historyErr)
		return
	}
	if counted.IsZero() {
		p("VMs      not counted yet by a running server")
	} else {
		p("VMs      %d awake, %d frozen by the memory brake, %d asleep in memory, %d asleep on disk, %d asleep with nothing kept",
			r.VMs.Awake, r.VMs.Frozen, r.VMs.Paused, r.VMs.Saved, r.VMs.Stopped)
		p("         (counted by the server %s ago, at %s; it counts at least every minute while it runs)", now.Sub(counted).Round(time.Second), counted.Format(time.RFC3339))
	}
	refusal := func(what string, last *protocol.ServerCapacityRefusal) {
		if last == nil {
			p("last refused %s: none in 14 days", what)
			return
		}
		detail := ""
		if last.Op == "wake-auto" {
			detail = ", a wake the server tried by itself"
		}
		if last.RunID != "" {
			detail += ", session " + last.RunID
		}
		p("last refused %s: %s%s\n  %s", what, last.At, detail, last.Text)
	}
	refusal("launch", r.LastLaunchRefusal)
	refusal("wake", r.LastWakeRefusal)
	p("")
	p("%-34s %10s %10s", "counted, in whole hours (UTC)", "24 hours", "14 days")
	day := map[[3]string]protocol.ServerCapacityCount{}
	for _, c := range r.Day {
		day[[3]string{c.Kind, c.Op, c.Cause}] = c
	}
	var dayRefused, refused, dayWaited, waited protocol.ServerCapacityCount
	for _, c := range r.Fortnight {
		d := day[[3]string{c.Kind, c.Op, c.Cause}]
		name := c.Kind
		for _, part := range []string{c.Op, c.Cause} {
			if part != "" {
				name += " " + part
			}
		}
		p("%-34s %10d %10d", name, d.Count, c.Count)
		switch {
		case c.Kind == domain.CapacityRefusal && (c.Cause == "memory" || c.Cause == "stall"):
			dayRefused.UsedBytes, dayRefused.IdleHourBytes = dayRefused.UsedBytes+d.UsedBytes, dayRefused.IdleHourBytes+d.IdleHourBytes
			refused.UsedBytes, refused.IdleHourBytes = refused.UsedBytes+c.UsedBytes, refused.IdleHourBytes+c.IdleHourBytes
		case c.Kind == domain.CapacityWaitEnd && c.Cause == "started":
			dayWaited.Count, dayWaited.WaitedSeconds, dayWaited.MaxWaitedSeconds = dayWaited.Count+d.Count, dayWaited.WaitedSeconds+d.WaitedSeconds, max(dayWaited.MaxWaitedSeconds, d.MaxWaitedSeconds)
			waited.Count, waited.WaitedSeconds, waited.MaxWaitedSeconds = waited.Count+c.Count, waited.WaitedSeconds+c.WaitedSeconds, max(waited.MaxWaitedSeconds, c.MaxWaitedSeconds)
		}
	}
	if len(r.Fortnight) == 0 {
		p("nothing counted: no refusal, wait, freeze or save for memory was recorded")
		return
	}
	share := func(c protocol.ServerCapacityCount) string {
		if c.UsedBytes == 0 {
			return "-"
		}
		return fmt.Sprintf("%.0f%%", 100*float64(c.IdleHourBytes)/float64(c.UsedBytes))
	}
	p("")
	p("%-34s %10s %10s", "memory held by session VMs idle over an hour or closed, of the memory in use at memory refusals", share(dayRefused), share(refused))
	wait := func(c protocol.ServerCapacityCount) string {
		if c.Count == 0 {
			return "-"
		}
		return fmt.Sprintf("%s mean, %s longest", time.Duration(c.WaitedSeconds/c.Count)*time.Second, time.Duration(c.MaxWaitedSeconds)*time.Second)
	}
	p("waits that ended in a start, 24 hours: %s", wait(dayWaited))
	p("waits that ended in a start, 14 days:  %s", wait(waited))
}
