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
	err := Replace(path, func(tempPath string) error {
		file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		if err == nil {
			err = Sync(file)
		}
		return errors.Join(err, file.Close())
	})
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// Replace calls create to build path's replacement at a temporary path next
// to it, then renames it over path. Nothing is flushed to disk.
func Replace(path string, create func(tempPath string) error) error {
	tempPath := newTempPath(path)
	err := create(tempPath)
	if err == nil {
		err = os.Rename(tempPath, path)
	}
	if err != nil {
		os.RemoveAll(tempPath)
		return err
	}
	return nil
}

// newTempPath returns an unused name next to path for building its replacement.
// The name is random, so it never matches a file the user created.
func newTempPath(path string) string {
	suffix := make([]byte, 6)
	rand.Read(suffix)
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".undo-"+hex.EncodeToString(suffix))
}

// syncDir flushes a directory's entries to disk, so a rename into it survives
// a crash.
func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return Sync(file)
}

// Sync flushes file's data to the disk. It uses fsync(2) and not
// os.File.Sync, which on macOS also flushes the drive's own cache and takes
// about a hundred times longer. That keeps checkpoints fast, at the cost of
// the last write if the power fails.
func Sync(file *os.File) error {
	return unix.Fsync(int(file.Fd()))
}
