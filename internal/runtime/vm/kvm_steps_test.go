//go:build kvm

package vm

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMLaunchAndWakeLogTheirSteps launches, saves and restores a VM under
// timed contexts and reads the engine's steps from the lines the scheduler
// would log.
func TestKVMLaunchAndWakeLogTheirSteps(t *testing.T) {
	e := kvmEngine(t)
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	steps := func(msg string, want []string) {
		t.Helper()
		var line string
		for l := range strings.Lines(out.String()) {
			if strings.Contains(l, msg) {
				line = l
			}
		}
		t.Log(strings.TrimSpace(line))
		at := 0
		for _, name := range want {
			i := strings.Index(line[at:], " "+name+"_ms=")
			if i < 0 {
				t.Fatalf("%q lacks the step %s after the ones before it: %s", msg, name, line)
			}
			at += i
		}
	}

	const key = "run-vmtest-steps"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		CPULimit: 1, MemoryLimitBytes: 512 << 20,
	}
	ctx := runtime.WithSteps(t.Context())
	if _, err := e.create(ctx, key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(ctx, key); err != nil {
		t.Fatal(err)
	}
	runtime.LogSteps(ctx, "launch steps")
	steps("launch steps", []string{"image", "disks", "checkout_disk", "prepare", "helper_disk", "network", "unit", "hypervisor", "guest_boot", "guest_setup", "helper_boot"})

	if err := e.save(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	ctx = runtime.WithSteps(t.Context())
	if err := e.restore(ctx, key); err != nil {
		t.Fatal(err)
	}
	runtime.LogSteps(ctx, "wake steps")
	steps("wake steps", []string{"network", "unit", "hypervisor", "restore", "resume", "helper"})
}
