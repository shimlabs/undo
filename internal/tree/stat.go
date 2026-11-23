package tree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// Stat describes the file at path. If it is a symlink, the link itself is
// described, not its target.
func Stat(path string) (Entry, error) {
	entry, _, err := statAt(unix.AT_FDCWD, path)
	return entry, err
}

// StatAt describes name in dir, as Stat does. Going through the opened
// directory means a parent swapped for a symlink cannot make it describe a
// file elsewhere.
func StatAt(dir *os.File, name string) (Entry, error) {
	entry, _, err := statAt(int(dir.Fd()), name)
	return entry, err
}

// fileID is what tells one file from every other: its device and inode.
type fileID struct {
	device uint64
	inode  uint64
}

func statAt(dirFd int, name string) (Entry, fileID, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Entry{}, fileID{}, &fs.PathError{Op: "fstatat", Path: name, Err: err}
	}
	mode := fileMode(toUint32(stat.Mode))
	entry := Entry{
		Mode:       mode,
		Size:       stat.Size,
		ModTime:    stat.Mtim.Nano(),
		ChangeTime: stat.Ctim.Nano(),
		Inode:      stat.Ino,
	}
	switch {
	case mode.IsRegular():
		entry.Kind = File
		// Only a count above one is recorded, so a file with a single name
		// compares equal to listings saved before the count existed.
		if stat.Nlink > 1 {
			entry.LinkCount = toUint64(stat.Nlink)
		}
	case mode.IsDir():
		entry.Kind = Dir
		entry.Size = 0
	case mode&fs.ModeSymlink != 0:
		entry.Kind = Symlink
		link, err := readlinkAt(dirFd, name)
		if err != nil {
			return Entry{}, fileID{}, err
		}
		entry.Link = link
	default:
		entry.Kind = Other
	}
	return entry, fileID{device: toUint64(stat.Dev), inode: stat.Ino}, nil
}

func readlinkAt(dirFd int, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buffer := make([]byte, size)
		n, err := unix.Readlinkat(dirFd, name, buffer)
		if err != nil {
			return "", &fs.PathError{Op: "readlinkat", Path: name, Err: err}
		}
		if n < size {
			return string(buffer[:n]), nil
		}
	}
}

// fileMode converts a stat mode the way os.Lstat does, so entries compare
// equal to those in listings saved before.
func fileMode(mode uint32) fs.FileMode {
	fileMode := fs.FileMode(mode & 0o777)
	switch mode & unix.S_IFMT {
	case unix.S_IFBLK:
		fileMode |= fs.ModeDevice
	case unix.S_IFCHR:
		fileMode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFDIR:
		fileMode |= fs.ModeDir
	case unix.S_IFIFO:
		fileMode |= fs.ModeNamedPipe
	case unix.S_IFLNK:
		fileMode |= fs.ModeSymlink
	case unix.S_IFSOCK:
		fileMode |= fs.ModeSocket
	}
	if mode&unix.S_ISGID != 0 {
		fileMode |= fs.ModeSetgid
	}
	if mode&unix.S_ISUID != 0 {
		fileMode |= fs.ModeSetuid
	}
	if mode&unix.S_ISVTX != 0 {
		fileMode |= fs.ModeSticky
	}
	return fileMode
}

// OpenDir opens the directory rel below root one name at a time, never
// following a symlink, so a directory an agent swaps for a symlink cannot
// lead outside root. A symlink or file in place of a directory means the
// directory is gone, and the error matches fs.ErrNotExist.
func OpenDir(root, rel string) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(root, err)
	}
	if rel != "." {
		for name := range strings.SplitSeq(rel, "/") {
			child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if err != nil {
				return nil, openError(path.Join(root, rel), err)
			}
			fd = child
		}
	}
	return os.NewFile(uintptr(fd), path.Join(root, rel)), nil
}

func openError(name string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	return &fs.PathError{Op: "open", Path: name, Err: err}
}

// toUint32 converts a stat mode, whose type differs between platforms.
func toUint32[T ~uint16 | ~uint32](mode T) uint32 {
	return uint32(mode)
}

// toUint64 converts a link count or device number, whose types differ
// between platforms.
func toUint64[T ~int32 | ~uint16 | ~uint32 | ~uint64](n T) uint64 {
	return uint64(n)
}
