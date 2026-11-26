package vm

import (
	"fmt"
	"os"
	"strings"
)

// shmemHugePages is the host's huge page policy for shared memory, which
// backs a guest's RAM.
const shmemHugePages = "/sys/kernel/mm/transparent_hugepage/shmem_enabled"

// adviseHugePages moves the policy in the file at path from never to
// advise, under which shared memory that asks for huge pages gets them, as
// Cloud Hypervisor asks for a guest's RAM. It returns the policy it found,
// and leaves any other one as it is: deny is an administrator's refusal and
// the rest already serve the request.
func adviseHugePages(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("vm runtime: read the huge page policy: %w", err)
	}
	_, rest, _ := strings.Cut(string(data), "[")
	was, _, ok := strings.Cut(rest, "]")
	if !ok {
		return "", fmt.Errorf("vm runtime: %s names no policy in force: %q", path, data)
	}
	if was != "never" {
		return was, nil
	}
	if err := os.WriteFile(path, []byte("advise"), 0o644); err != nil {
		return was, fmt.Errorf("vm runtime: set the huge page policy: %w", err)
	}
	return was, nil
}
