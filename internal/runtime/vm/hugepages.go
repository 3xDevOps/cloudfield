package vm

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const hugePages = "/sys/kernel/mm/transparent_hugepage"

// adviseHugePages moves the host's huge page policy for shared memory, which
// backs a guest's RAM, from never to advise in the settings directory dir.
// Under advise shared memory that asks for huge pages gets them, as Cloud
// Hypervisor asks for a guest's RAM. It returns the policy it found, and
// leaves any other one as it is: deny is an administrator's refusal and the
// rest already serve the request. Unless the policy is deny it first sets
// khugepaged/max_ptes_none to 0: at the kernel's 511, khugepaged merges any
// 2 MiB range that holds one page into a huge page, which takes back from
// the host every page of the range the guest had returned. It logs each
// setting it changes, with the value it replaced.
func adviseHugePages(dir string) (string, error) {
	path := filepath.Join(dir, "shmem_enabled")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("vm runtime: read the huge page policy: %w", err)
	}
	_, rest, _ := strings.Cut(string(data), "[")
	was, _, ok := strings.Cut(rest, "]")
	if !ok {
		return "", fmt.Errorf("vm runtime: %s names no policy in force: %q", path, data)
	}
	if was == "deny" {
		return was, nil
	}
	holes := filepath.Join(dir, "khugepaged/max_ptes_none")
	if data, err = os.ReadFile(holes); err != nil {
		return was, fmt.Errorf("vm runtime: stop khugepaged filling memory a guest returned: %w", err)
	}
	if found := strings.TrimSpace(string(data)); found != "0" {
		if err = os.WriteFile(holes, []byte("0"), 0o644); err != nil {
			return was, fmt.Errorf("vm runtime: stop khugepaged filling memory a guest returned: %w", err)
		}
		slog.Info("vm runtime: set how empty a range the host's khugepaged merges into a huge page", "file", holes, "was", found, "now", "0")
	}
	if was != "never" {
		return was, nil
	}
	if err = os.WriteFile(path, []byte("advise"), 0o644); err != nil {
		return was, fmt.Errorf("vm runtime: set the huge page policy: %w", err)
	}
	slog.Info("vm runtime: set the host's huge page policy for shared memory", "file", path, "was", was, "now", "advise")
	return was, nil
}
