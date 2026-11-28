//go:build kvm

package vm

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// TestKVMSessionThatFillsTmpHitsItsOwnDisk has a session's user fill /tmp.
// The data disk gives it close to dataDiskBytes, the write past that fails
// with the guest's own ENOSPC, and the disk's host file stays the size it
// was made. The git service, whose store is on the same disk, still
// snapshots and commits the checkout from what the disk keeps for root.
func TestKVMSessionThatFillsTmpHitsItsOwnDisk(t *testing.T) {
	h := newCheckoutHost(t)
	ws := domain.WorkspaceID("wsscratch")
	base := h.workspace(t, ws, 20)
	key := fmt.Sprintf("run-vmscratch-%x", time.Now().Unix())
	h.session(t, ws, key, base, runtime.Mount{HostPath: t.TempDir(), ContainerPath: "/tmp", Kind: runtime.MountScratch})
	rec, err := h.e.load(key)
	if err != nil {
		t.Fatal(err)
	}
	// boot gives the session 1 GiB of memory.
	size := dataDiskBytes + swapBytes(1<<10, false)
	onHost := func() int64 {
		t.Helper()
		info, statErr := os.Stat(rec.Data.Path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		return info.Size()
	}
	if got := onHost(); got != size {
		t.Fatalf("the data disk is %d bytes, want %d", got, size)
	}
	room, err := strconv.ParseInt(h.sh(t, key, "echo $(( $(stat -f -c %a /tmp) * $(stat -f -c %S /tmp) ))"), 10, 64)
	if err != nil || room > dataDiskBytes || room < dataDiskBytes-4<<30 {
		t.Fatalf("/tmp gives the session's user %d bytes, %v; want within 4 GiB under %d", room, err, int64(dataDiskBytes))
	}
	// fallocate takes blocks in the guest without writing them, so the fill
	// costs the host only what the last write puts down.
	fill := fmt.Sprintf("fallocate -l %d /tmp/fill && head -c 128M /dev/zero > /tmp/rest", room-64<<20)
	code, out, errOut, err := h.e.exec(t.Context(), key, []string{"/bin/sh", "-c", fill}, "/workspace")
	if err != nil || code == 0 || !strings.Contains(errOut, "No space left on device") {
		t.Fatalf("a write past the end of /tmp: exit %d, %v: %s%s; want the guest's No space left on device", code, err, out, errOut)
	}
	if got := onHost(); got != size {
		t.Errorf("after the fill the data disk is %d bytes, want the %d it was made with", got, size)
	}

	h.sh(t, key, "echo changed >> src/0/f0.txt")
	waitDiff(t, h.diffs, "src/0/f0.txt", 3*time.Minute)
	commit, err := h.git.CommitAll(t.Context(), domain.RunID(key), "aether: a task", domain.GitIdentity{Name: "Member", Email: "member@localhost"}, nil)
	if err != nil || commit == "" {
		t.Fatalf("final commit of a session whose user filled /tmp = %q, %v", commit, err)
	}
	if head := h.sh(t, key, "git rev-parse HEAD"); head != commit {
		t.Errorf("the checkout's HEAD is %s, want the commit %s", head, commit)
	}
}
