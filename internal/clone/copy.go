//go:build darwin || linux

package clone

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// CopyAt copies srcName in srcDir to name in dir, including a whole
// directory, keeping modes and modification times. It is for filesystems
// that cannot clone. It works relative to both directories and never follows
// a symlink, so a symlink placed in either tree cannot lead it elsewhere. It
// fails if name already exists.
func CopyAt(srcDir *os.File, srcName string, dir *os.File, name string) error {
	return copyTreeAt(srcDir, srcName, dir, name, copyContent)
}

// copyContent copies the bytes of in to out.
func copyContent(in, out *os.File) error {
	_, err := io.Copy(out, in)
	return err
}

// copyTreeAt recreates srcName in srcDir as name in dir and fills each
// regular file with copyData. Sockets and devices are skipped.
func copyTreeAt(srcDir *os.File, srcName string, dir *os.File, name string, copyData func(in, out *os.File) error) error {
	srcFd, dirFd := int(srcDir.Fd()), int(dir.Fd())
	var stat unix.Stat_t
	if err := unix.Fstatat(srcFd, srcName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "fstatat", Path: srcName, Err: err}
	}
	perm := toUint32(stat.Mode) & 0o7777
	var err error
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		err = copyFileAt(srcFd, srcName, dirFd, name, perm, copyData)
	case unix.S_IFDIR:
		err = copyDirAt(srcFd, srcName, dir, name, perm, copyData)
	case unix.S_IFLNK:
		err = copySymlinkAt(srcFd, srcName, dirFd, name)
	case unix.S_IFIFO:
		err = MkfifoAt(dir, name, fs.FileMode(perm))
	default:
		return nil
	}
	if err != nil {
		return err
	}
	modTime := stat.Mtim
	if err := unix.UtimesNanoAt(dirFd, name, []unix.Timespec{modTime, modTime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "utimensat", Path: name, Err: err}
	}
	return nil
}

func copyFileAt(srcFd int, srcName string, dirFd int, name string, perm uint32, copyData func(in, out *os.File) error) error {
	// A file swapped for a named pipe since it was listed would block a
	// plain open, and is no longer the file to copy.
	inFd, err := unix.Openat(srcFd, srcName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return openatError(srcName, err)
	}
	in := os.NewFile(uintptr(inFd), srcName)
	defer in.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(inFd, &stat); err != nil {
		return &os.PathError{Op: "fstat", Path: srcName, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return &os.PathError{Op: "copy", Path: srcName, Err: unix.ENOENT}
	}
	outFd, err := unix.Openat(dirFd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return openatError(name, err)
	}
	out := os.NewFile(uintptr(outFd), name)
	err = copyData(in, out)
	if err == nil {
		if chmodErr := unix.Fchmod(outFd, perm); chmodErr != nil {
			err = &os.PathError{Op: "fchmod", Path: name, Err: chmodErr}
		}
	}
	err = errors.Join(err, out.Close())
	if err != nil {
		err = errors.Join(err, unix.Unlinkat(dirFd, name, 0))
	}
	return err
}

func copyDirAt(srcFd int, srcName string, dir *os.File, name string, perm uint32, copyData func(in, out *os.File) error) error {
	dirFd := int(dir.Fd())
	if err := unix.Mkdirat(dirFd, name, 0o700); err != nil {
		return &os.PathError{Op: "mkdirat", Path: name, Err: err}
	}
	src, err := openDirAt(srcFd, srcName)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := openDirAt(dirFd, name)
	if err != nil {
		return err
	}
	defer dst.Close()
	entries, err := src.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		err := copyTreeAt(src, entry.Name(), dst, entry.Name(), copyData)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := unix.Fchmod(int(dst.Fd()), perm); err != nil {
		return &os.PathError{Op: "fchmod", Path: name, Err: err}
	}
	return nil
}

func copySymlinkAt(srcFd int, srcName string, dirFd int, name string) error {
	buffer := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(srcFd, srcName, buffer)
	if err != nil {
		return &os.PathError{Op: "readlinkat", Path: srcName, Err: err}
	}
	if err := unix.Symlinkat(string(buffer[:n]), dirFd, name); err != nil {
		return &os.PathError{Op: "symlinkat", Path: name, Err: err}
	}
	return nil
}

// openDirAt opens the directory name in dirFd without following a symlink.
func openDirAt(dirFd int, name string) (*os.File, error) {
	fd, err := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openatError(name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// toUint32 converts a file mode, whose type differs between platforms.
func toUint32[T ~uint16 | ~uint32](mode T) uint32 {
	return uint32(mode)
}

// PermissionBits returns the part of mode that chmod sets.
func PermissionBits(mode fs.FileMode) fs.FileMode {
	return mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}

func openatError(name string, err error) error {
	return &os.PathError{Op: "openat", Path: name, Err: err}
}
