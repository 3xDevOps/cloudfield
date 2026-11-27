package serversetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteConfig replaces the file by a rename: the mode stays, a symlink
// stays a symlink with its target rewritten, and a write that fails leaves
// the old file and no temporary file.
func TestWriteConfigReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(path, map[string]string{"edge-url": "https://edge.cloudfield.ai"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after the write %v, %v; want 0600", info.Mode(), err)
	}
	if got, lerr := Load(path); lerr != nil || got["edge-url"] != "https://edge.cloudfield.ai" {
		t.Fatalf("config %v, %v", got, lerr)
	}

	realDir := t.TempDir()
	real := filepath.Join(realDir, "server.conf")
	if err = os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.conf")
	if err = os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(link, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	if li, lerr := os.Lstat(link); lerr != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link after the write: %v, %v; want it kept", li.Mode(), lerr)
	}
	if got, lerr := Load(real); lerr != nil || got["addr"] != ":2300" {
		t.Fatalf("target after the write: %v, %v", got, lerr)
	}

	// A rename onto a directory that is not empty fails after the
	// temporary file is written.
	blocked := filepath.Join(dir, "blocked.conf")
	if err = os.MkdirAll(filepath.Join(blocked, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(blocked, map[string]string{"addr": ":2300"}); err == nil {
		t.Fatal("write onto a directory succeeded")
	}
	if _, err = os.Stat(filepath.Join(blocked, "keep")); err != nil {
		t.Errorf("the old content is gone: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}

// Root writes a file another user owns in place: a rename would create a
// file with that owner in whatever directory the path names by then.
func TestWriteConfigAsRootKeepsAnotherOwnersFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the file was replaced by a rename")
	}
	if uid, _ := fileOwner(after); uid != 65534 {
		t.Errorf("owner after the write %d, want 65534", uid)
	}
	if got, lerr := Load(path); lerr != nil || got["addr"] != ":2300" {
		t.Fatalf("config %v, %v", got, lerr)
	}
}

// A config shared with a group other than the writer's own keeps it.
func TestWriteConfigKeepsTheGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.conf")
	if err = os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	gid := -1
	for _, g := range groups {
		if g != os.Getegid() && os.Chown(path, -1, g) == nil {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("needs a second group")
	}
	if err = WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, got := fileOwner(info); got != gid {
		t.Errorf("group after the write %d, want %d", got, gid)
	}
}

// The config lock sits beside the file a path resolves to, mode 0600, and
// under root belongs to the config file's owner, so a service that owns
// its config can take it too.
func TestConfigLockBesideTheRealFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "server.conf")
	if err = os.WriteFile(real, []byte("addr = :2300\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(real, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias.conf")
	if err = os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(alias, map[string]string{"addr": ":2301"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(real + ".lock")
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("lock file: %v, %v", info, err)
	}
	if uid, _ := fileOwner(info); os.Geteuid() == 0 && uid != 65534 {
		t.Errorf("lock file owner %d, want the config's 65534", uid)
	}
	if _, err = os.Lstat(alias + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a lock beside the alias: %v", err)
	}
}

// A lock name planted as a hard link to another file is refused, and that
// file keeps its owner and mode.
func TestConfigLockRefusesAPlantedLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(path, []byte("addr = :2300\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Link(victim, path+".lock"); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	err = WriteConfig(path, map[string]string{"addr": ":2301"})
	if err == nil || !strings.Contains(err.Error(), "is not a regular file with one link owned by root or the config's owner") {
		t.Fatalf("write with a planted lock: %v", err)
	}
	after, err := os.Stat(victim)
	if err != nil || after.Mode() != before.Mode() {
		t.Fatalf("the linked file changed: %v, %v", after, err)
	}
	if uid, _ := fileOwner(after); uid != os.Geteuid() {
		t.Errorf("the linked file's owner is %d", uid)
	}
	if got, lerr := Load(path); lerr != nil || got["addr"] != ":2300" {
		t.Errorf("config after the refusal: %v, %v", got, lerr)
	}
}
