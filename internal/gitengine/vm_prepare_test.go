//go:build integration

package gitengine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// keptDisks records which prepared disk each slot keeps, and fills every
// disk it is asked to keep.
type keptDisks struct {
	dir    string
	mu     sync.Mutex
	kept   map[string]string
	forgot []string
}

func (d *keptDisks) NewCheckoutDisk(_ context.Context, _ string, _ int64, _ func(context.Context, string) error, path string) error {
	return os.WriteFile(path, nil, 0o600)
}

func (d *keptDisks) KeepCheckoutDisk(ctx context.Context, group, slot, key string, _ int64, fill func(context.Context, string) error) error {
	stage := filepath.Join(d.dir, key)
	if _, err := os.Stat(stage); err != nil {
		if err = os.Mkdir(stage, 0o755); err != nil {
			return err
		}
		if err = fill(ctx, stage); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.kept[group+"/"+slot] = key
	return nil
}

func (d *keptDisks) KeptCheckoutDisks(group string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var slots []string
	for name := range d.kept {
		if filepath.Dir(name) == group {
			slots = append(slots, filepath.Base(name))
		}
	}
	return slots, nil
}

func (d *keptDisks) ForgetCheckoutDisks(group string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.forgot = append(d.forgot, group)
	return nil
}

func TestMirrorAdvancePreparesTheCheckoutDiskOfTheNewBase(t *testing.T) {
	e := newTestEngine(t, nil)
	disks := &keptDisks{dir: t.TempDir(), kept: map[string]string{}}
	e.cfg.VM = &VM{Disks: disks}
	ctx := t.Context()
	source := filepath.Join(t.TempDir(), "source.git")
	gitc(t, filepath.Dir(source), "init", "--bare", source)
	work := filepath.Join(t.TempDir(), "work")
	gitc(t, filepath.Dir(work), "init", work)
	push := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitc(t, work, "add", "-A")
		gitc(t, work, "commit", "-m", name)
		gitc(t, work, "push", source, "HEAD:refs/heads/main")
	}
	push("one.txt")
	const ws = domain.WorkspaceID("mirror")
	if _, err := e.InitWorkspaceRepo(ctx, ws); err != nil {
		t.Fatal(err)
	}
	e.cfg.MirrorFetch = func(ctx context.Context, repo string, req MirrorRequest, incoming string) error {
		return exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", req.SourceURL, "+refs/heads/"+req.Branch+":"+incoming).Run()
	}
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(ctx, ws, req); err != nil {
		t.Fatal(err)
	}
	observed, err := e.RefreshWorkspaceMirror(ctx, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.AdoptWorkspaceMirror(ctx, ws, 1, observed.CandidateCommit); err != nil {
		t.Fatal(err)
	}
	first := observed.CandidateCommit
	uid, gid := os.Getuid(), os.Getgid()
	slot := "mirror/" + strconv.Itoa(uid) + "-" + strconv.Itoa(gid)
	key := func(base string) string {
		return "checkout-mirror-" + base + "-" + strconv.Itoa(uid) + "-" + strconv.Itoa(gid)
	}
	launch := func(run domain.RunID, base string) {
		t.Helper()
		checkout, _, createErr := e.CreateRunCheckoutAt(ctx, ws, run, base, "main", "a task", "")
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, _, ok, diskErr := e.VMCheckoutDisk(ctx, checkout, "/workspace", uid, gid); diskErr != nil || !ok {
			t.Fatalf("checkout disk = %v, %v", ok, diskErr)
		}
	}
	kept := func() string {
		disks.mu.Lock()
		defer disks.mu.Unlock()
		return disks.kept[slot]
	}

	e.preparing.Wait()
	if kept() != "" {
		t.Fatalf("a workspace no session started in keeps %q", kept())
	}
	launch("run-first", first)
	if kept() != key(first) {
		t.Fatalf("after a launch from the base, kept = %q, want %q", kept(), key(first))
	}

	push("two.txt")
	advanced, err := e.RefreshWorkspaceMirror(ctx, ws, req)
	if err != nil || advanced.BaseCommit == first {
		t.Fatalf("refresh = %+v, %v", advanced, err)
	}
	e.preparing.Wait()
	if kept() != key(advanced.BaseCommit) {
		t.Fatalf("after the mirror advanced, kept = %q, want %q", kept(), key(advanced.BaseCommit))
	}
	prepared := filepath.Join(disks.dir, key(advanced.BaseCommit), PreparedDir)
	if head := gitc(t, prepared, "rev-parse", "HEAD"); head != advanced.BaseCommit {
		t.Fatalf("the disk prepared ahead is at %s, want %s", head, advanced.BaseCommit)
	}
	if _, err = os.Stat(filepath.Join(prepared, "two.txt")); err != nil {
		t.Fatal(err)
	}

	launch("run-older", first)
	if kept() != key(advanced.BaseCommit) {
		t.Fatalf("a launch from an older commit took the kept slot: %q", kept())
	}

	if err = e.RemoveWorkspaceRepo(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(disks.forgot, []string{"mirror"}) {
		t.Fatalf("forgot = %v, want the removed workspace", disks.forgot)
	}
}
