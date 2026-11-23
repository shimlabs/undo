// Package clone makes copy-on-write copies of files and directories.
package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// File clones src to dst with FICLONE, so the copy shares its data with src.
// Directories are cloned file by file. A symlink is cloned as a link, not
// followed. It fails if dst already exists, and returns an error matching
// errors.ErrUnsupported when the filesystem cannot clone.
func File(src, dst string) error {
	err := copyTree(src, dst, func(src, dst *os.File) error {
		err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
		if err != nil {
			return &os.LinkError{Op: "ficlone", Old: src.Name(), New: dst.Name(), Err: unsupported(err)}
		}
		return nil
	})
	if err != nil && errors.Is(err, errors.ErrUnsupported) {
		os.RemoveAll(dst)
	}
	return err
}

// unsupported marks the errors FICLONE returns on filesystems without clones,
// or across filesystems, as errors.ErrUnsupported.
func unsupported(err error) error {
	for _, errno := range []error{unix.EXDEV, unix.EINVAL, unix.ENOTTY, unix.EOPNOTSUPP, unix.ENOSYS} {
		if errors.Is(err, errno) {
			return errors.Join(errors.ErrUnsupported, err)
		}
	}
	return err
}
