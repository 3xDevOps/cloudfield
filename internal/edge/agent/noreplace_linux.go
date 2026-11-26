package edgeagent

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldname to newname unless newname exists.
func renameNoReplace(oldname, newname string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, oldname, unix.AT_FDCWD, newname, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "renameat2", Old: oldname, New: newname, Err: err}
	}
	return nil
}
