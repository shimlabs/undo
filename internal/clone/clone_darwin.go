// Package clone makes copy-on-write copies of files and directories, and
// clears the lock flags that clones keep.
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

// CanCloneDirs reports whether FileAt clones a whole directory in one call.
const CanCloneDirs = true

// FileAt clones srcName in srcDir to name in dir without copying the data. A
// whole directory is cloned in one call, and a symlink is cloned as a link.
// It works relative to both directories, so no symlink above either name is
// followed. It fails if name already exists, and returns an error matching
// errors.ErrUnsupported when the volume cannot clone.
func FileAt(srcDir *os.File, srcName string, dir *os.File, name string) error {
	if err := unix.Clonefileat(int(srcDir.Fd()), srcName, int(dir.Fd()), name, unix.CLONE_NOFOLLOW); err != nil {
		if errors.Is(err, unix.EXDEV) {
			err = errors.Join(errors.ErrUnsupported, err)
		}
		return &os.LinkError{Op: "clonefileat", Old: srcName, New: name, Err: err}
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

// HasAppleDoubleFiles reports whether macOS keeps extended attributes in "._"
// files on the filesystem that holds dir.
func HasAppleDoubleFiles(dir string) bool {
	var stat unix.Statfs_t
	return unix.Statfs(dir, &stat) == nil && isAppleDoubleFilesystem(stat)
}

// isAppleDoubleFilesystem reports whether stat is of a filesystem without
// extended attributes of its own, on which macOS keeps a file's attributes in
// a "._" file beside it.
func isAppleDoubleFilesystem(stat unix.Statfs_t) bool {
	switch unix.ByteSliceToString(stat.Fstypename[:]) {
	case "msdos", "exfat", "smbfs", "webdav":
		return true
	}
	return false
}

// IsSupportedIn reports whether the filesystem that holds dir can clone
// files. It only reads the filesystem's type, so it changes nothing.
func IsSupportedIn(dir string) bool {
	var stat unix.Statfs_t
	if unix.Statfs(dir, &stat) != nil {
		return true
	}
	return unix.ByteSliceToString(stat.Fstypename[:]) == "apfs"
}
