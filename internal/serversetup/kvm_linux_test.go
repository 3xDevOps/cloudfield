package serversetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckKVMShowsTheRealError(t *testing.T) {
	err := CheckKVM(filepath.Join(t.TempDir(), "kvm"))
	if err == nil || !strings.Contains(err.Error(), "no such file or directory") || !strings.Contains(err.Error(), "docs/install.md#hosts-for-vm-sessions") {
		t.Errorf("CheckKVM of a missing device = %v", err)
	}
}

func TestCheckKVMRefusesADeviceThatIsNotKVM(t *testing.T) {
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := CheckKVM(device)
	if err == nil || !strings.Contains(err.Error(), "inappropriate ioctl for device") {
		t.Errorf("CheckKVM of a regular file = %v", err)
	}
}
