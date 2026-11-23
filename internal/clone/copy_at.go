//go:build darwin || linux

package clone

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// CopyAt copies the file or symlink src to name in dir, keeping its mode and
// modification time. It works relative to dir, so it never follows a symlink
// that replaced a directory above dir. It fails if name already exists.
func CopyAt(src string, dir *os.File, name string) error {
	return copyAt(src, dir, name, func(in, out *os.File) error {
		_, err := io.Copy(out, in)
		return err
	})
}

// copyAt recreates the file or symlink src as name in dir and fills a regular
// file with copyData.
func copyAt(src string, dir *os.File, name string, copyData func(in, out *os.File) error) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dirFd := int(dir.Fd())
	switch mode := info.Mode(); {
	case mode.IsRegular():
		err = copyFileAt(src, dirFd, name, info, copyData)
	case mode&fs.ModeSymlink != 0:
		var target string
		if target, err = os.Readlink(src); err == nil {
			err = unix.Symlinkat(target, dirFd, name)
		}
	default:
		return &os.PathError{Op: "copy", Path: src, Err: errors.ErrUnsupported}
	}
	if err != nil {
		return err
	}
	modTime := unix.NsecToTimespec(info.ModTime().UnixNano())
	return unix.UtimesNanoAt(dirFd, name, []unix.Timespec{modTime, modTime}, unix.AT_SYMLINK_NOFOLLOW)
}

func copyFileAt(src string, dirFd int, name string, info fs.FileInfo, copyData func(in, out *os.File) error) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fd, err := unix.Openat(dirFd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &os.PathError{Op: "openat", Path: name, Err: err}
	}
	out := os.NewFile(uintptr(fd), name)
	err = copyData(in, out)
	if err == nil {
		err = out.Chmod(PermissionBits(info.Mode()))
	}
	err = errors.Join(err, out.Close())
	if err != nil {
		err = errors.Join(err, unix.Unlinkat(dirFd, name, 0))
	}
	return err
}
