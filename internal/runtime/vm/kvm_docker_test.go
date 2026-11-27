//go:build kvm

package vm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMDockerAnswersRightAfterLaunch launches a session on an image with
// dockerd as an unprivileged user and runs docker at once: the launch did
// not wait for dockerd, and the command is answered when dockerd serves.
func TestKVMDockerAnswersRightAfterLaunch(t *testing.T) {
	e := kvmEngine(t)
	image := os.Getenv("AETHER_TEST_IMAGE")
	if image == "" {
		t.Skip("set AETHER_TEST_IMAGE to an image with dockerd")
	}
	const key = "run-vmtest-docker"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: image, CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"}, User: "1000:1000", NestedDocker: true,
		Env: map[string]string{"AETHER_RUN_ID": key, "HOME": "/home/aether"}, CPULimit: 1, MemoryLimitBytes: 1 << 30,
	}
	if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	started := time.Since(began)
	out, code := vmRun(t, e, key, "test -r /var/log/aether-dockerd.log && docker info --format '{{.ServerVersion}}'")
	answered := time.Since(began)
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("dockerd's log and docker info right after the launch: exit %d: %s", code, out)
	}
	t.Logf("start returned after %v; docker info, run at once as uid 1000, was answered %v after the start began", started.Round(time.Millisecond), answered.Round(time.Millisecond))
}
