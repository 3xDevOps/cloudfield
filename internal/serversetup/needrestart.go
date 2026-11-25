package serversetup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// NeedrestartDir holds the configuration of needrestart, which Debian and
// Ubuntu run after a package upgrade to restart every service whose
// program or libraries were replaced on disk.
const NeedrestartDir = "/etc/needrestart"

// needrestartConf matches the unit names internal/runtime/vm gives VMs.
// needrestart evals each conf.d file as Perl; 0 means never restart.
const needrestartConf = `# Written by cloudfield-server; it rewrites this file when it starts.
# Restarting a session VM's unit ends the session it runs.
$nrconf{override_rc}{qr(^aether-vm-)} = 0;
`

// ExcludeVMsFromNeedrestart tells the needrestart configured in dir to
// leave session VM units alone, and returns the file that says so, or ""
// on a host without needrestart. A server update replaces the binary every
// VM's monitor runs, so needrestart would otherwise restart them all on
// the next package upgrade.
func ExcludeVMsFromNeedrestart(dir string) (string, error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("serversetup: needrestart: %w", err)
	}
	path := filepath.Join(dir, "conf.d", "cloudfield.conf")
	if have, err := os.ReadFile(path); err == nil && string(have) == needrestartConf {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("serversetup: needrestart: %w", err)
	}
	if err := os.WriteFile(path, []byte(needrestartConf), 0o644); err != nil {
		return "", fmt.Errorf("serversetup: needrestart: %w", err)
	}
	return path, nil
}
