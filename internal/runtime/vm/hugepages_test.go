package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdviseHugePagesOnlyRaisesNever(t *testing.T) {
	for _, tc := range []struct{ content, was, after string }{
		{"always within_size advise [never] deny force\n", "never", "advise"},
		{"always within_size [advise] never deny force\n", "advise", "always within_size [advise] never deny force\n"},
		{"always [within_size] advise never deny force\n", "within_size", "always [within_size] advise never deny force\n"},
		{"always within_size advise never [deny] force\n", "deny", "always within_size advise never [deny] force\n"},
	} {
		path := filepath.Join(t.TempDir(), "shmem_enabled")
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		was, err := adviseHugePages(path)
		if err != nil || was != tc.was {
			t.Errorf("%q: found %q, %v, want %q", tc.content, was, err, tc.was)
		}
		if after, _ := os.ReadFile(path); string(after) != tc.after {
			t.Errorf("%q: the file holds %q, want %q", tc.content, after, tc.after)
		}
	}
	if _, err := adviseHugePages(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a host without the policy file: no error")
	}
}

func TestPlainPagesChangesOnlyTheHugePageRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"cpus":{"boot_vcpus":1},"memory":{"size":9311354880,"shared":true,"thp":true},"disks":[{"path":"/d"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plainPages(dir); err != nil {
		t.Fatal(err)
	}
	const want = `{"cpus":{"boot_vcpus":1},"disks":[{"path":"/d"}],"memory":{"shared":true,"size":9311354880,"thp":false}}`
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Errorf("the saved configuration reads %s, want %s", got, want)
	}
}
