package serversetup

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// From linux/kvm.h; golang.org/x/sys/unix defines neither.
const (
	kvmCheckExtension = 0xAE03
	kvmCapUserMemory2 = 231
)

// CheckKVM opens device read-write, as Cloud Hypervisor does, and asks it
// for KVM_CAP_USER_MEMORY2: the pinned Cloud Hypervisor exits without it,
// and Linux has it from 6.8.
func CheckKVM(device string) error {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("session VMs need KVM: %w; run the server on bare metal or on a cloud VM with nested virtualization (docs/install.md#hosts-for-vm-sessions)", err)
	}
	defer func() { _ = f.Close() }()
	has, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), kvmCheckExtension, kvmCapUserMemory2)
	if errno != 0 {
		return fmt.Errorf("session VMs need KVM: KVM_CHECK_EXTENSION on %s: %w", device, errno)
	}
	if has == 0 {
		return fmt.Errorf("session VMs need Linux 6.8 or newer: %s has no KVM_CAP_USER_MEMORY2, which %s requires (docs/install.md#hosts-for-vm-sessions)", device, CloudHypervisor.Name)
	}
	return nil
}
