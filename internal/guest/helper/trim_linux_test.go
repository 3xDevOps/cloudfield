package helper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrimOnce(t *testing.T) {
	for _, tc := range []struct {
		name, stall, inactive, want string
	}{
		{"cold cache", "0.00", "1140850688", "268435456 swappiness=0"},
		{"little cache", "0.00", "67108864", ""},
		{"stalled", "1.00", "1140850688", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"memory.pressure": "some avg10=0.00 avg60=" + tc.stall + " avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
				"memory.stat":     "anon 1\nfile 2\ninactive_file " + tc.inactive + "\n",
				"memory.reclaim":  "",
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := trimOnce(dir); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "memory.reclaim"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("memory.reclaim got %q, want %q", got, tc.want)
			}
		})
	}
}
