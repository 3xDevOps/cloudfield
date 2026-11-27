package vm

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdviseHugePagesOnlyRaisesNever(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		content, was, after, holes string
		logged                     []string
	}{
		{"always within_size advise [never] deny force\n", "never", "advise", "0", []string{"max_ptes_none was=511 now=0", "shmem_enabled was=never now=advise"}},
		{"always within_size [advise] never deny force\n", "advise", "always within_size [advise] never deny force\n", "0", []string{"max_ptes_none was=511 now=0"}},
		{"always [within_size] advise never deny force\n", "within_size", "always [within_size] advise never deny force\n", "0", []string{"max_ptes_none was=511 now=0"}},
		{"always within_size advise never [deny] force\n", "deny", "always within_size advise never [deny] force\n", "511\n", nil},
	} {
		dir := t.TempDir()
		path, holes := filepath.Join(dir, "shmem_enabled"), filepath.Join(dir, "khugepaged/max_ptes_none")
		if err := errors.Join(os.Mkdir(filepath.Dir(holes), 0o755), os.WriteFile(holes, []byte("511\n"), 0o644), os.WriteFile(path, []byte(tc.content), 0o644)); err != nil {
			t.Fatal(err)
		}
		was, err := adviseHugePages(dir)
		if err != nil || was != tc.was {
			t.Errorf("%q: found %q, %v, want %q", tc.content, was, err, tc.was)
		}
		if after, _ := os.ReadFile(path); string(after) != tc.after {
			t.Errorf("%q: the file holds %q, want %q", tc.content, after, tc.after)
		}
		if after, _ := os.ReadFile(holes); string(after) != tc.holes {
			t.Errorf("%q: max_ptes_none holds %q, want %q", tc.content, after, tc.holes)
		}
		for _, want := range tc.logged {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("%q: the log reads %q, want %q in it", tc.content, logs.String(), want)
			}
		}
		if strings.Count(logs.String(), "\n") != len(tc.logged) {
			t.Errorf("%q: the log reads %q, want %d lines", tc.content, logs.String(), len(tc.logged))
		}
		logs.Reset()
		if tc.was == "never" {
			continue
		}
		if _, err = adviseHugePages(dir); err != nil || logs.Len() != 0 {
			t.Errorf("%q: a second start logs %q, %v; want nothing left to change", tc.content, logs.String(), err)
		}
	}
	if _, err := adviseHugePages(t.TempDir()); err == nil {
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
	for _, saved := range []string{`{"memory":null,"disks":[]}`, `{"disks":[]}`} {
		if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := plainPages(dir); err == nil {
			t.Errorf("%s: no error", saved)
		}
	}
}
