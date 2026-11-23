package clone

import (
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

// UnlockAt clears the lock flags the owner may clear from name in dir,
// without following a symlink.
func UnlockAt(dir *os.File, name string) error {
	// Most files are not locked, and one stat is much cheaper than opening
	// each of them.
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	if stat.Flags&userLockFlags == 0 {
		return nil
	}
	// O_SYMLINK opens a symlink itself, which clonefile also locks.
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_SYMLINK|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return openatError(name, err)
	}
	defer unix.Close(fd)
	// The name may point to another file since the first stat, so the flags
	// to keep are read from the opened one.
	if err := unix.Fstat(fd, &stat); err != nil {
		return &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if err := unix.Fchflags(fd, int(stat.Flags&^userLockFlags)); err != nil {
		return &os.PathError{Op: "fchflags", Path: name, Err: err}
	}
	return nil
}
