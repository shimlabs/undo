// Package clone makes copy-on-write copies of files and directories.
package clone

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// CanCloneDirs reports whether File and FileAt clone a whole directory in one
// call. FICLONE clones one file at a time.
const CanCloneDirs = false

// File clones src to dst with FICLONE, so the copy shares its data with src.
// Directories are cloned file by file. A symlink is cloned as a link, not
// followed. It fails if dst already exists, and returns an error matching
// errors.ErrUnsupported when the filesystem cannot clone.
func File(src, dst string) error {
	err := copyTree(src, dst, cloneContent)
	if err != nil && errors.Is(err, errors.ErrUnsupported) {
		os.RemoveAll(dst)
	}
	return err
}

// FileAt clones the file or symlink src to name in dir with FICLONE. It works
// relative to dir, so it never follows a symlink that replaced a directory
// above dir.
func FileAt(src string, dir *os.File, name string) error {
	return copyAt(src, dir, name, cloneContent)
}

// markUnsupported marks the errors FICLONE returns on filesystems without
// clones, or across filesystems, as errors.ErrUnsupported.
func markUnsupported(err error) error {
	for _, errno := range []error{unix.EXDEV, unix.EINVAL, unix.ENOTTY, unix.EOPNOTSUPP, unix.ENOSYS} {
		if errors.Is(err, errno) {
			return errors.Join(errors.ErrUnsupported, err)
		}
	}
	return err
}

// MkfifoAt creates the named pipe name in dir. It works relative to dir, so
// it never follows a symlink that replaced a directory above dir.
func MkfifoAt(dir *os.File, name string, perm fs.FileMode) error {
	if err := unix.Mkfifoat(int(dir.Fd()), name, uint32(perm.Perm())); err != nil {
		return &os.PathError{Op: "mkfifoat", Path: name, Err: err}
	}
	return nil
}

// cloneContent makes out share the data of in with FICLONE.
func cloneContent(in, out *os.File) error {
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		return &os.LinkError{Op: "ficlone", Old: in.Name(), New: out.Name(), Err: markUnsupported(err)}
	}
	return nil
}
