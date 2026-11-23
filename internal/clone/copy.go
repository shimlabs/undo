//go:build darwin || linux

package clone

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Copy copies src to dst, including whole directories, keeping modes and
// modification times. A symlink is copied as a link, not followed. It fails if
// dst already exists. It is for filesystems that cannot clone.
func Copy(src, dst string) error {
	return copyTree(src, dst, func(in, out *os.File) error {
		_, err := io.Copy(out, in)
		return err
	})
}

// copyTree recreates src at dst and fills each regular file with copyData.
func copyTree(src, dst string, copyData func(in, out *os.File) error) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode().IsRegular():
		err = copyFile(src, dst, info, copyData)
	case info.IsDir():
		err = copyDir(src, dst, info, copyData)
	case info.Mode()&fs.ModeSymlink != 0:
		var target string
		if target, err = os.Readlink(src); err == nil {
			err = os.Symlink(target, dst)
		}
	case info.Mode()&fs.ModeNamedPipe != 0:
		err = unix.Mkfifo(dst, uint32(info.Mode().Perm()))
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return setTimes(dst, info)
}

func copyFile(src, dst string, info fs.FileInfo, copyData func(in, out *os.File) error) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = copyData(in, out)
	if err == nil {
		err = out.Chmod(PermissionBits(info.Mode()))
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

func copyDir(src, dst string, info fs.FileInfo, copyData func(in, out *os.File) error) error {
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), copyData)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.Chmod(dst, PermissionBits(info.Mode()))
}

func setTimes(path string, info fs.FileInfo) error {
	modTime := unix.NsecToTimespec(info.ModTime().UnixNano())
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, []unix.Timespec{modTime, modTime}, unix.AT_SYMLINK_NOFOLLOW)
}

// PermissionBits returns the part of mode that chmod sets.
func PermissionBits(mode fs.FileMode) fs.FileMode {
	return mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}
