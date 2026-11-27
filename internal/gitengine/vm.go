package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/guest"
)

// VM is where checkouts that live on their session VM's disks come from.
type VM struct {
	Disks VMDisks
	// Guest returns the git service of the VM named vm, the session's run
	// ID; it fails with guest.ErrVMNotRunning when the VM is known not to
	// run.
	Guest func(ctx context.Context, vm domain.RunID) (*guest.Git, error)
}

// VMDisks is the part of the VM image store checkout disks come from.
type VMDisks interface {
	NewCheckoutDisk(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error, path string) error
	KeepCheckoutDisk(ctx context.Context, group, slot, key string, size int64, fill func(ctx context.Context, dir string) error) error
	KeptCheckoutDisks(group string) ([]string, error)
	ForgetCheckoutDisks(group string) error
}

const (
	// vmCheckoutDiskBytes is the size of every checkout filesystem. The
	// disks are sparse, so only what a session writes takes space.
	vmCheckoutDiskBytes = 128 << 30
	vmCheckoutDisk      = "disk.qcow2"
	// PreparedBranch is the branch a prepared checkout disk is on. The VM
	// renames it to the run branch on the checkout's first boot.
	PreparedBranch = "aether-prepared"
	// PreparedDir is the checkout's directory on a prepared disk, which
	// keeps the filesystem's lost+found out of it.
	PreparedDir = "checkout"
)

// createVMCheckout claims the run branch and records the checkout; its
// disk is made when the VM is created, which knows the user who owns it.
func (e *Engine) createVMCheckout(ctx context.Context, repo string, ws domain.WorkspaceID, run domain.RunID, checkoutPath, baseCommit, baseBranch, task, origin string) (string, string, error) {
	branch, err := e.uniqueRunBranch(ctx, repo, baseCommit, run, task)
	if err != nil {
		return "", "", err
	}
	release := func() {
		_ = os.RemoveAll(checkoutPath)
		if _, delErr := e.git(ctx, repo, "update-ref", "-d", "refs/heads/"+branch, baseCommit); delErr != nil {
			slog.Warn("gitengine: release reserved run branch", "run", run, "branch", branch, "error", delErr)
		}
	}
	if err := os.Mkdir(checkoutPath, 0o700); err != nil {
		release()
		return "", "", fmt.Errorf("gitengine: create checkout for session %s: %w", run, err)
	}
	meta := runMeta{Base: baseCommit, Branch: branch, BaseBranch: baseBranch, Workspace: ws, VM: true, Origin: origin}
	if err := e.writeRunMeta(run, meta); err != nil {
		release()
		return "", "", err
	}
	return checkoutPath, branch, nil
}

// IsVMCheckout reports whether run's checkout lives on a VM disk.
func (e *Engine) IsVMCheckout(run domain.RunID) bool {
	meta, err := e.readRunMeta(run)
	return err == nil && meta.VM
}

// VMCheckoutDisk returns the qcow2 disk of the checkout at path, a checkout
// path CreateRunCheckoutAt returned, which the VM mounts at target, making it
// on first use: a copy-on-write disk over the prepared disk of the
// checkout's workspace and base, whose files belong to uid:gid. ok is false
// for a checkout on the server's disk.
func (e *Engine) VMCheckoutDisk(ctx context.Context, path, target string, uid, gid int) (string, guest.Checkout, bool, error) {
	key, err := e.checkoutRunID(filepath.Clean(path))
	if err != nil {
		return "", guest.Checkout{}, false, nil
	}
	meta, err := e.readRunMeta(key)
	if errors.Is(err, ErrRunMetadataNotFound) {
		return "", guest.Checkout{}, false, nil
	}
	if err != nil {
		return "", guest.Checkout{}, false, err
	}
	if !meta.VM {
		return "", guest.Checkout{}, false, nil
	}
	if e.cfg.VM == nil {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: session %s has its checkout in a VM, and this server runs no VMs", key)
	}
	id := guest.Checkout{
		Key: string(key), Workspace: string(meta.Workspace), Branch: meta.Branch,
		Base: meta.Base, BaseBranch: meta.BaseBranch, Origin: meta.Origin, UID: uid, GID: gid,
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	if meta.Target != target {
		meta.Target = target
		if err = e.writeRunMeta(key, meta); err != nil {
			return "", guest.Checkout{}, false, err
		}
	}
	disk := filepath.Join(path, vmCheckoutDisk)
	if _, statErr := os.Stat(disk); statErr == nil {
		return disk, id, true, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: checkout disk of session %s: %w", key, statErr)
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return "", guest.Checkout{}, false, err
	}
	prepared, fill := e.preparedCheckout(meta.Workspace, repo, meta.Base, uid, gid)
	if err = e.cfg.VM.Disks.NewCheckoutDisk(ctx, prepared, vmCheckoutDiskBytes, fill, disk); err != nil {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: checkout disk of session %s: %w", key, err)
	}
	// Only the base branch's own commit is kept: a session from an older
	// commit or from its parent session's branch must not take the place of
	// the disk the next launch needs.
	if meta.BaseBranch != "" && !isRunBranch(meta.BaseBranch) && readRefBestEffort(ctx, e, repo, "refs/heads/"+meta.BaseBranch) == meta.Base {
		if err = e.cfg.VM.Disks.KeepCheckoutDisk(ctx, string(meta.Workspace), preparedSlot(uid, gid), prepared, vmCheckoutDiskBytes, fill); err != nil {
			return "", guest.Checkout{}, false, fmt.Errorf("gitengine: keep the prepared checkout disk of session %s: %w", key, err)
		}
	}
	return disk, id, true, nil
}

func preparedSlot(uid, gid int) string { return strconv.Itoa(uid) + "-" + strconv.Itoa(gid) }

func (e *Engine) preparedCheckout(ws domain.WorkspaceID, repo, base string, uid, gid int) (string, func(context.Context, string) error) {
	key := "checkout-" + string(ws) + "-" + base + "-" + preparedSlot(uid, gid)
	return key, func(ctx context.Context, dir string) error {
		return e.fillPreparedCheckout(ctx, repo, base, filepath.Join(dir, PreparedDir), uid, gid)
	}
}

// prepareCheckouts builds, in the background, the prepared checkout disk of
// the base branch's new commit for every owner a session of ws started as,
// so the next launch does not build it.
func (e *Engine) prepareCheckouts(ws domain.WorkspaceID, repo, base string) {
	if e.cfg.VM == nil {
		return
	}
	slots, err := e.cfg.VM.Disks.KeptCheckoutDisks(string(ws))
	if err != nil {
		slog.Warn("gitengine: list kept checkout disks", "workspace", ws, "error", err)
		return
	}
	if len(slots) == 0 {
		return
	}
	e.preparing.Go(func() {
		for _, slot := range slots {
			if e.pruneCtx.Err() != nil {
				return
			}
			var uid, gid int
			if _, scanErr := fmt.Sscanf(slot, "%d-%d", &uid, &gid); scanErr != nil {
				slog.Warn("gitengine: kept checkout disk has no owner", "workspace", ws, "slot", slot, "error", scanErr)
				continue
			}
			key, fill := e.preparedCheckout(ws, repo, base, uid, gid)
			if keepErr := e.cfg.VM.Disks.KeepCheckoutDisk(e.pruneCtx, string(ws), slot, key, vmCheckoutDiskBytes, fill); keepErr != nil {
				slog.Warn("gitengine: prepare checkout disk", "workspace", ws, "base", base, "error", keepErr)
			}
		}
	})
}

// fillPreparedCheckout makes the checkout a prepared disk holds: a
// self-contained clone at base on PreparedBranch, owned by uid:gid. The
// clone copies the objects, so the ownership change never reaches the
// workspace repository.
func (e *Engine) fillPreparedCheckout(ctx context.Context, repo, base, dir string, uid, gid int) error {
	if _, err := e.git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--no-checkout", repo, dir); err != nil {
		return err
	}
	if _, err := e.git(ctx, dir, "checkout", "--quiet", "-b", PreparedBranch, base); err != nil {
		return err
	}
	return ChownCheckout(dir, uid, gid)
}

// ChownCheckout gives every file of the checkout at dir to uid:gid. It is
// for a checkout on a disk of its own, which shares no file with the
// workspace repository.
func ChownCheckout(dir string, uid, gid int) error {
	return filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

// publishVM publishes the run branch at the HEAD of the checkout in the VM.
func (e *Engine) publishVM(ctx context.Context, run domain.RunID, meta runMeta) (string, error) {
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return "", err
	}
	head, err := g.Head(ctx, string(run))
	if err != nil {
		return "", vmErr(err)
	}
	return head, e.publishVMHead(ctx, run, meta, head)
}

// removeVMCheckout deletes the checkout's disk and identity. Its snapshot
// store keeps the history, detached first: it keeps borrowing the fork
// point from the workspace repository, pinned there, and holds the rest.
func (e *Engine) removeVMCheckout(ctx context.Context, run domain.RunID, checkout, metaPath string) error {
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	if err := e.detachVMStore(ctx, run); err != nil {
		return err
	}
	if err := e.cfg.RemoveCheckout(ctx, checkout); err != nil {
		return fmt.Errorf("gitengine: remove checkout disk of session %s: %w", run, err)
	}
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gitengine: remove session metadata for %s: %w", run, err)
	}
	return nil
}

// VMSyncRoot is where a live overlay of run's checkout is served, when that
// checkout lives in a VM.
func (e *Engine) VMSyncRoot(run domain.RunID) (string, bool) {
	meta, err := e.readRunMeta(run)
	if err != nil || !meta.VM || meta.Target == "" {
		return "", false
	}
	return meta.Target, true
}

// ServeVMSync relays a live overlay's stream, already pinned to the root
// VMSyncRoot names, to the endpoint the VM serves at the checkout.
func (e *Engine) ServeVMSync(ctx context.Context, run domain.RunID, stream io.ReadWriteCloser) error {
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return err
	}
	endpoint, err := g.Sync(ctx, string(run))
	if err != nil {
		return vmErr(err)
	}
	defer func() { _ = endpoint.Close() }()
	// Whichever side ends first closes the other: the VM's endpoint would
	// otherwise wait for a client that left, keeping its sync slot.
	up := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(endpoint, stream)
		up <- copyErr
		_ = endpoint.Close()
	}()
	_, err = io.Copy(stream, endpoint)
	select {
	case upErr := <-up:
		_ = stream.Close()
		return upErr
	default:
	}
	_ = stream.Close()
	_ = endpoint.Close()
	<-up
	return err
}

func (e *Engine) detachVMStore(ctx context.Context, run domain.RunID) error {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(store, "HEAD")); errors.Is(statErr, fs.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return err
	}
	if err = e.retainRecordedSnapshotTrees(ctx, meta.Workspace, run, store); err != nil {
		return err
	}
	repo, err := e.repoPath(meta.Workspace)
	if err != nil {
		return err
	}
	// Evidence that read the final tree before this removal may still read it.
	var keep []string
	if meta.FinalTree != "" && e.hasObject(ctx, store, meta.FinalTree) {
		keep = append(keep, meta.FinalTree)
	}
	if err = e.shareHistory(ctx, run, store, repo, keep...); err != nil {
		return fmt.Errorf("gitengine: detach the history of session %s: %w", run, err)
	}
	return nil
}
