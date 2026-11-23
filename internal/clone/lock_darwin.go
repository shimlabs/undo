package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

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
