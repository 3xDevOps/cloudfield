package edgeagent

import "golang.org/x/sys/unix"

// renameNoReplace renames oldname to newname unless newname exists.
func renameNoReplace(oldname, newname string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldname, unix.AT_FDCWD, newname, unix.RENAME_NOREPLACE)
}
