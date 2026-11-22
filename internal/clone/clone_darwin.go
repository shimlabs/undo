// Package clone makes copy-on-write copies of files and directories.
package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// File clones src to dst without copying the data. A whole directory is
// cloned in one call. A symlink is cloned as a link, not followed. It fails if
// dst already exists, and returns an error matching errors.ErrUnsupported when
// the volume cannot clone.
func File(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		if err == unix.EXDEV {
			err = errors.Join(errors.ErrUnsupported, err)
		}
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	return nil
}
