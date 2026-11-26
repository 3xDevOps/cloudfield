package localops

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/syncd"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// unitArgs reads back the argv every OS's unit was rendered with,
// quoting included.
func TestUnitArgsRoundTrip(t *testing.T) {
	args := []string{"--server", "host:2222", "--repo", `/home/a b/it's "x" $HOME %h\`, "--server-id", "srv",
		"--edge-url", "https://edge.example.test", "--key", "", "--workspace", "w&<x>"}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		unit, err := syncd.ServiceUnit(goos, "/opt/cloud field/aether", append([]string{"daemon", "run"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		got, err := unitArgs(goos, unit.Content)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if !slices.Equal(got, args) {
			t.Errorf("%s: args = %q, want %q", goos, got, args)
		}
	}
	if _, err := unitArgs("linux", "[Service]\nExecStart=/bin/true\n"); err == nil {
		t.Error("a unit that does not run the daemon was read as one")
	}
}

func TestInstalledDaemonArgs(t *testing.T) {
	testhome.Isolate(t)
	args, err := InstalledDaemonArgs()
	if err != nil || args != nil {
		t.Fatalf("before install: %q, %v", args, err)
	}
	repo := t.TempDir()
	if _, _, err := InstallDaemon(syncd.Config{ServerID: "srv", EdgeURL: "https://edge.example.test", RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	args, err = InstalledDaemonArgs()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--repo", repo, "--server-id", "srv", "--edge-url", "https://edge.example.test"}; !slices.Equal(args, want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	_, path, err := DaemonStatus()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstalledDaemonArgs(); err == nil {
		t.Fatalf("a damaged unit at %s was read", filepath.Base(path))
	}
}
