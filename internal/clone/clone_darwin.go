// Package clone makes copy-on-write copies of files and directories.
package clone

/*
#include <errno.h>
#include <stdlib.h>
#include <sys/stat.h>

static int undoMkfifoat(int fd, const char *name, mode_t mode) {
	if (__builtin_available(macOS 13.0, *)) {
		return mkfifoat(fd, name, mode);
	}
	errno = ENOTSUP;
	return -1;
}
*/
import "C"

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

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

// MkfifoAt creates the named pipe name in dir. It works relative to dir, so
// it never follows a symlink that replaced a directory above dir. macOS
// before 13 has no mkfifoat, so there it creates the pipe by its path.
func MkfifoAt(dir *os.File, name string, perm fs.FileMode) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	if status, err := C.undoMkfifoat(C.int(dir.Fd()), cName, C.mode_t(perm.Perm())); status != 0 {
		if errors.Is(err, unix.ENOTSUP) {
			return unix.Mkfifo(filepath.Join(dir.Name(), name), uint32(perm.Perm()))
		}
		return &os.PathError{Op: "mkfifoat", Path: name, Err: err}
	}
	return nil
}

// lockFlags stop a file from being changed, renamed or removed. Finder's
// Locked checkbox sets UF_IMMUTABLE, and clonefile copies the flags.
const lockFlags = unix.UF_IMMUTABLE | unix.UF_APPEND | unix.SF_IMMUTABLE | unix.SF_APPEND

// userLockFlags are the lock flags the file's owner may clear.
const userLockFlags = unix.UF_IMMUTABLE | unix.UF_APPEND

// IsLocked reports whether path has a flag that stops it from being changed,
// renamed or removed.
func IsLocked(path string) bool {
	var stat unix.Stat_t
	return unix.Lstat(path, &stat) == nil && stat.Flags&lockFlags != 0
}

// Unlock clears the lock flags the owner may clear from path. A symlink is
// left alone, since chflags would follow it.
func Unlock(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	if stat.Flags&userLockFlags == 0 || stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil
	}
	if err := unix.Chflags(path, int(stat.Flags&^userLockFlags)); err != nil {
		return &os.PathError{Op: "chflags", Path: path, Err: err}
	}
	return nil
}

// UnlockAt clears the lock flags the owner may clear from name in dir,
// without following a symlink.
func UnlockAt(dir *os.File, name string) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "openat", Path: name, Err: err}
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if stat.Flags&userLockFlags == 0 {
		return nil
	}
	if err := unix.Fchflags(fd, int(stat.Flags&^userLockFlags)); err != nil {
		return &os.PathError{Op: "fchflags", Path: name, Err: err}
	}
	return nil
}
