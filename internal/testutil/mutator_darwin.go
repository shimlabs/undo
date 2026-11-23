package testutil

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func exchangePaths(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_SWAP)
}

// setLocked sets or clears the user immutable flag on path, and reports
// whether it could.
func setLocked(path string, isLocked bool) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	var flags int
	if isLocked {
		flags = unix.UF_IMMUTABLE
	}
	return unix.Chflags(path, flags) == nil
}
