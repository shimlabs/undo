// Package atomicfile replaces files so that readers see either the old
// content or the new content, never a partial write, even after a crash.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// WriteFile writes data to path with perm. The data goes to a temporary file
// next to path first, is flushed to disk, and is then renamed over path.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(WriteFileIn(root, filepath.Base(path), data, perm), root.Close())
}

// WriteFileIn is WriteFile for name inside root, so a symlink cannot lead the
// write outside it.
func WriteFileIn(root *os.Root, name string, data []byte, perm os.FileMode) error {
	tempName := filepath.Join(filepath.Dir(name), TempName(filepath.Base(name)))
	file, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = Sync(file)
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = root.Rename(tempName, name)
	}
	if err != nil {
		return errors.Join(err, root.Remove(tempName))
	}
	dir, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(Sync(dir), dir.Close())
}

// TempName returns an unused name in the same directory as name for building
// its replacement. The name is random, so it never matches a file the user
// created.
func TempName(name string) string {
	suffix := make([]byte, 6)
	rand.Read(suffix)
	return "." + name + ".undo-" + hex.EncodeToString(suffix)
}

// Sync flushes file's data to the disk. It uses fsync(2) and not
// os.File.Sync, which on macOS also flushes the drive's own cache and takes
// about a hundred times longer. That keeps checkpoints fast, at the cost of
// the last write if the power fails.
func Sync(file *os.File) error {
	return unix.Fsync(int(file.Fd()))
}
