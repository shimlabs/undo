// Package clone makes copy-on-write copies of files and directories.
package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CanCloneDirs reports whether File and FileAt clone a whole directory in one
// call.
const CanCloneDirs = true

// File clones src to dst without copying the data. A whole directory is
// cloned in one call. A symlink is cloned as a link, not followed. It fails if
// dst already exists, and returns an error matching errors.ErrUnsupported when
// the volume cannot clone.
func File(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		if errors.Is(err, unix.EXDEV) {
			err = errors.Join(errors.ErrUnsupported, err)
		}
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	return nil
}

// FileAt clones src to name in dir, like File. It works relative to dir, so
// it never follows a symlink that replaced a directory above dir.
func FileAt(src string, dir *os.File, name string) error {
	if err := unix.Clonefileat(unix.AT_FDCWD, src, int(dir.Fd()), name, unix.CLONE_NOFOLLOW); err != nil {
		if errors.Is(err, unix.EXDEV) {
			err = errors.Join(errors.ErrUnsupported, err)
		}
		return &os.LinkError{Op: "clonefileat", Old: src, New: name, Err: err}
	}
	return nil
}
