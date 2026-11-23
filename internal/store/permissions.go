package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shimlabs/undo/internal/clone"
)

// makeWritable lets the store add and remove entries in dir. Directories in
// the base keep the project's modes, and a read-only one would otherwise stop
// merges. Their recorded modes live in the listings, not on disk.
func makeWritable(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o700 == 0o700 {
		return err
	}
	return os.Chmod(dir, info.Mode().Perm()|0o700)
}

// removeAll removes path and everything below it, including directories that
// were cloned read-only and files whose lock flags a whole-directory clone
// kept.
func removeAll(path string) error {
	err := os.RemoveAll(path)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// Directories that cannot be made writable make the final RemoveAll fail,
	// and that error is the one to report.
	_ = filepath.WalkDir(path, func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_ = clone.Unlock(entryPath)
		if entry.IsDir() {
			_ = makeWritable(entryPath)
		}
		return nil
	})
	if err := makeWritable(filepath.Dir(path)); err != nil {
		return err
	}
	return os.RemoveAll(path)
}
