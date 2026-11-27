package helper

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/guest"
)

// A session whose checkout is in a VM takes on a task's branch while the VM
// runs, and a watch that started before the rename publishes on the new one.
func TestRenameRunBranchMovesTheVMCheckout(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	onBranch := func(branch string) {
		t.Helper()
		if head := runGit(t, p.checkout, "symbolic-ref", "--short", "HEAD"); head != branch {
			t.Fatalf("checkout is on %s, want %s", head, branch)
		}
		if got := runGit(t, p.checkout, "config", "aether.branch"); got != branch {
			t.Fatalf("aether.branch = %s, want %s", got, branch)
		}
	}
	refs := func() string {
		t.Helper()
		return runGit(t, p.repo, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/cloudfield/")
	}
	before := refs()

	p.down.Store(true)
	if _, err := p.host.RenameRunBranch(ctx, testRun, "another task"); !errors.Is(err, guest.ErrVMNotRunning) {
		t.Fatalf("rename while the VM is down = %v, want %v", err, guest.ErrVMNotRunning)
	}
	p.down.Store(false)
	onBranch(p.branch)

	// A branch of that name in the checkout fails the rename in the VM.
	runGit(t, p.checkout, "branch", "cloudfield/run-blocked-ses1")
	if _, err := p.host.RenameRunBranch(ctx, testRun, "blocked"); err == nil {
		t.Fatal("rename onto a branch the checkout has succeeded")
	}
	onBranch(p.branch)
	if after := refs(); after != before {
		t.Fatalf("failed renames left the session branches\n%s\nwant\n%s", after, before)
	}
	runGit(t, p.checkout, "branch", "-D", "cloudfield/run-blocked-ses1")

	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	branch, err := p.host.RenameRunBranch(ctx, testRun, "another task")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rename took %s", time.Since(start))
	if branch != "cloudfield/run-another-task-ses1" {
		t.Fatalf("renamed to %s", branch)
	}
	onBranch(branch)
	if got := refs(); got != branch+" "+p.base {
		t.Fatalf("session branches after the rename:\n%s", got)
	}

	runGit(t, p.checkout, "commit", "--quiet", "--allow-empty", "-m", "work")
	head := runGit(t, p.checkout, "rev-parse", "HEAD")
	for deadline := time.Now().Add(15 * time.Second); runGitQuiet(p.repo, "rev-parse", "refs/heads/"+branch) != head; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never moved to the checkout's commit %s; session branches:\n%s", branch, head, refs())
		}
	}
	if got := refs(); got != branch+" "+head {
		t.Fatalf("session branches after the publish:\n%s", got)
	}

	if _, err = p.host.RenameRunBranch(ctx, testRun, "third task"); err == nil {
		t.Fatal("rename of a published branch succeeded")
	}
	onBranch(branch)
	if got := refs(); got != branch+" "+head {
		t.Fatalf("a refused rename left the session branches\n%s", got)
	}
}
