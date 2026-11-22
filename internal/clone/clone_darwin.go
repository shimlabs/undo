// Package clone makes copy-on-write copies of files and directories.
package clone

import (
	"os"

	"golang.org/x/sys/unix"
)

// File clones src to dst without copying the data. A symlink is cloned as a
// link, not followed. It fails if dst already exists.
func File(src, dst string) error {
	return clonefile(src, dst)
}

// Tree clones the whole directory at src to dst in a single call.
func Tree(src, dst string) error {
	return clonefile(src, dst)
}

func clonefile(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	return nil
}
