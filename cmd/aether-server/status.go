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
	o, _, rest, err := loadOptions("status", args, nil)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(statusUsage)
	}
	db, err := store.Open(server.StorePath(*o.dataDir))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // only read
	ctx, now := context.Background(), time.Now()
	vms, counted, err := db.VMCounts(ctx)
	if err != nil {
		return err
	}
	hours, err := db.CapacityCounts(ctx, now.Add(-sshd.CapacityFortnight))
	if err != nil {
		return err
	}
	// No pid when the unit is not running: the server's own stall is then
	// left out.
	pid, _ := strconv.Atoi(unitPID())
	printStatus(os.Stdout, sshd.CapacityResult(scheduler.MeasureMachine(pid, *o.dataDir, *o.minFreeDisk), vms, hours, now), counted)
	return nil
}

// printStatus prints r; counted is when the server last counted its
// session VMs, zero when it never did.
func printStatus(w io.Writer, r protocol.ServerCapacityResult, counted time.Time) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	if r.MemoryError != "" {
		p("memory   not measured: %s", r.MemoryError)
	} else {
		p("memory   %s available of %s; the server keeps %s, and a launch needs %s above that", gib(r.MemoryAvailableBytes), gib(r.MemoryTotalBytes), gib(r.MemoryFloorBytes), gib(r.StartMemoryBytes))
	}
	stall := func(v *float64, limit float64, what, missing string) {
		if v == nil {
			p("stall    %s: not measured (%s)", what, missing)
			return
		}
		p("stall    %s: %.1f%%, limit %.0f%%", what, *v, limit)
	}
	stall(r.ServerStall, r.ServerStallLimit, "the server's cgroup over 60s, which refuses launches", "no running "+updateUnit+" unit with a cgroup of its own that reports memory.pressure")
	stall(r.HostStall, r.HostStallLimit, "the host over 10s, which saves session VMs", "the kernel reports no memory pressure")
	if r.StallError != "" {
		p("stall    not measured: %s", r.StallError)
	}
	if r.DiskError != "" {
		p("disk     not measured: %s", r.DiskError)
	}
	for _, d := range r.Disks {
		p("disk     %s: %s free of %s; the server keeps %s, and a launch needs %s above that", d.Name, gib(d.FreeBytes), gib(d.TotalBytes), gib(d.ReserveBytes), gib(r.StartDiskBytes))
	}
	if counted.IsZero() {
		p("VMs      not counted yet by a running server")
	} else {
		p("VMs      %d awake, %d frozen by the memory brake, %d asleep in memory, %d asleep on disk (counted %s)",
			r.VMs.Awake, r.VMs.Frozen, r.VMs.Paused, r.VMs.Saved, counted.Format(time.RFC3339))
	}
	refusal := func(what string, last *protocol.ServerCapacityRefusal) {
		if last == nil {
			p("last refused %s: none in 14 days", what)
			return
		}
		session := ""
		if last.RunID != "" {
			session = " session " + last.RunID
		}
		p("last refused %s: %s%s\n  %s", what, last.At, session, last.Text)
	}
	refusal("launch", r.LastLaunchRefusal)
	refusal("wake", r.LastWakeRefusal)
	p("")
	p("%-34s %10s %10s", "counted", "24 hours", "14 days")
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
		p("nothing: no launch or wake was refused, nothing waited, and no session VM was frozen or saved for memory")
		return
	}
	share := func(c protocol.ServerCapacityCount) string {
		if c.UsedBytes == 0 {
			return "-"
		}
		return fmt.Sprintf("%.0f%%", 100*float64(c.IdleHourBytes)/float64(c.UsedBytes))
	}
	p("")
	p("%-34s %10s %10s", "memory held by sessions idle over an hour, of the memory in use at memory refusals", share(dayRefused), share(refused))
	wait := func(c protocol.ServerCapacityCount) string {
		if c.Count == 0 {
			return "-"
		}
		return fmt.Sprintf("%s mean, %s longest", time.Duration(c.WaitedSeconds/c.Count)*time.Second, time.Duration(c.MaxWaitedSeconds)*time.Second)
	}
	p("waits that ended in a start, 24 hours: %s", wait(dayWaited))
	p("waits that ended in a start, 14 days:  %s", wait(waited))
}
