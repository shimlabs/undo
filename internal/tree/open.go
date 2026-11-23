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

// openDirFlags open a directory for reading its entries and opening names in
// it, and refuse a symlink in its place.
const openDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// ErrReplaced reports that the directory at a path changed while it was
// being opened.
var ErrReplaced = errors.New("changed while it was opened")

// OpenRoot opens dir as an os.Root and checks that the directory it opened is
// the one at dir. os.OpenRoot follows a symlink at dir itself, which another
// process could put there between a check and the open.
func OpenRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err == nil {
		var atPath fs.FileInfo
		if atPath, err = os.Lstat(dir); err == nil && !os.SameFile(opened, atPath) {
			err = fmt.Errorf("%s %w", dir, ErrReplaced)
		}
	}
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

// OpenDir opens the directory rel below root one name at a time, never
// following a symlink, so a directory an agent swaps for a symlink cannot
// lead outside root. A symlink or file in place of a directory means the
// directory is gone, and the error matches fs.ErrNotExist.
func OpenDir(root, rel string) (*os.File, error) {
	fd, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return nil, openError(root, err)
	}
	if rel != "." {
		for name := range strings.SplitSeq(rel, "/") {
			child, err := unix.Openat(fd, name, openDirFlags, 0)
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
