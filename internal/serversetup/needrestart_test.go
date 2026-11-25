package serversetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcludeVMsFromNeedrestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "needrestart")
	if path, err := ExcludeVMsFromNeedrestart(dir); path != "" || err != nil {
		t.Fatalf("on a host without needrestart = %q, %v", path, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a host without needrestart got %s: %v", dir, err)
	}

	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "conf.d", "cloudfield.conf")
	path, err := ExcludeVMsFromNeedrestart(dir)
	if path != want || err != nil {
		t.Fatalf("ExcludeVMsFromNeedrestart = %q, %v, want %q", path, err, want)
	}
	got, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(got), "\n$nrconf{override_rc}{qr(^aether-vm-)} = 0;\n") {
		t.Fatalf("%s = %q, %v", want, got, err)
	}

	// Read-only, so a second call that rewrote an up-to-date file fails.
	if err = os.Chmod(want, 0o444); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Dir(want), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(want), 0o755) })
	if path, err = ExcludeVMsFromNeedrestart(dir); path != want || err != nil {
		t.Fatalf("a second call = %q, %v", path, err)
	}

	if err = os.Chmod(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(want, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = ExcludeVMsFromNeedrestart(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(want); string(again) != string(got) {
		t.Errorf("an edited file was left as %q", again)
	}
}
