// Package clone makes copy-on-write copies of files and directories, and
// clears the lock flags that clones keep.
package clone

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// CanCloneDirs reports whether FileAt clones a whole directory in one call.
// FICLONE clones one file at a time.
const CanCloneDirs = false

// FileAt clones srcName in srcDir to name in dir with FICLONE, so the copy
// shares its data with the source. A directory is cloned file by file, and a
// symlink is cloned as a link. It works relative to both directories, so no
// symlink above either name is followed. It fails if name already exists,
// and returns an error matching errors.ErrUnsupported when the filesystem
// cannot clone.
func FileAt(srcDir *os.File, srcName string, dir *os.File, name string) error {
	return copyTreeAt(srcDir, srcName, dir, name, cloneContent)
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

// Filesystem types that support FICLONE, from statfs(2) and linux/magic.h.
const (
	btrfsMagic    = 0x9123683e
	xfsMagic      = 0x58465342
	bcachefsMagic = 0xca451a4e
)

// HasAppleDoubleFiles reports whether extended attributes are kept in "._"
// files on the filesystem that holds dir, which only macOS does.
func HasAppleDoubleFiles(string) bool { return false }

// IsSupportedIn reports whether the filesystem that holds dir can clone
// files. It only reads the filesystem's type, so it changes nothing; an XFS
// made without reflink still cannot clone.
func IsSupportedIn(dir string) bool {
	var stat unix.Statfs_t
	if unix.Statfs(dir, &stat) != nil {
		return true
	}
	switch uint64(stat.Type) { //nolint:gosec // filesystem magic numbers are 32-bit
	case btrfsMagic, xfsMagic, bcachefsMagic:
		return true
	}
	return false
}
