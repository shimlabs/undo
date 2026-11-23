package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

// openFiles opens dir as the root every file operation of the store goes
// through. A symlink placed inside the store, or the store directory swapped
// for another after it was checked, cannot lead a read or write outside it.
func openFiles(dir string) (*os.Root, error) {
	files, err := tree.OpenRoot(dir)
	if errors.Is(err, tree.ErrReplaced) {
		return nil, fmt.Errorf("%s: %w", dir, ErrUnsafeStore)
	}
	return files, err
}

// storeName returns path, which lies in the store directory, relative to it.
func (s *Store) storeName(path string) string {
	rel, err := filepath.Rel(s.dir, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *Store) readFile(path string) ([]byte, error) {
	return s.files.ReadFile(s.storeName(path))
}

func (s *Store) lstat(path string) (fs.FileInfo, error) {
	return s.files.Lstat(s.storeName(path))
}

func (s *Store) mkdirAll(path string) error {
	return s.files.MkdirAll(s.storeName(path), 0o700)
}

func (s *Store) remove(path string) error {
	return s.files.Remove(s.storeName(path))
}

func (s *Store) open(path string) (*os.File, error) {
	return s.files.Open(s.storeName(path))
}

// openFile opens path in the store with flag, creating it, if flag says so,
// readable only by the user.
func (s *Store) openFile(path string, flag int) (*os.File, error) {
	return s.files.OpenFile(s.storeName(path), flag, 0o600)
}

func (s *Store) openRoot(path string) (*os.Root, error) {
	return s.files.OpenRoot(s.storeName(path))
}

func (s *Store) chtimes(path string, modTime time.Time) error {
	return s.files.Chtimes(s.storeName(path), modTime, modTime)
}

func (s *Store) readDir(path string) ([]fs.DirEntry, error) {
	dir, err := s.open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// File.ReadDir stats every entry of a directory opened through a Root,
	// while a plain file for the same directory uses the types readdir
	// returns.
	fd, err := unix.Dup(int(dir.Fd()))
	if err != nil {
		return nil, &os.PathError{Op: "dup", Path: path, Err: err}
	}
	plain := os.NewFile(uintptr(fd), path)
	defer plain.Close()
	return plain.ReadDir(-1)
}

// writeFileAtomic replaces path in one rename, after flushing its new
// content to disk.
func (s *Store) writeFileAtomic(path string, data []byte) error {
	return atomicfile.WriteFileIn(s.files, s.storeName(path), data, 0o600)
}

// cloneAt copies srcName in srcDir to name in dir the way the store keeps
// files: as a clone, or as a full copy in copy mode. A clone keeps a locked
// file's lock flags, which would stop the store from replacing or removing
// its copy, so they are cleared on the copy.
func (s *Store) cloneAt(srcDir *os.File, srcName string, dir *os.File, name string) error {
	copyFile := clone.FileAt
	if s.IsCopyMode() {
		copyFile = clone.CopyAt
	}
	if err := copyFile(srcDir, srcName, dir, name); err != nil {
		return err
	}
	return clone.UnlockAt(dir, name)
}

// cloneIntoStore copies the project path rel to the store path dst.
func (s *Store) cloneIntoStore(rel, dst string) error {
	dir, err := s.open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	if rel == "." {
		// The project root is cloned by its own path, as it has no parent
		// inside the project to open.
		srcDir, err := os.Open(filepath.Dir(s.root))
		if err != nil {
			return err
		}
		defer srcDir.Close()
		return s.cloneAt(srcDir, filepath.Base(s.root), dir, filepath.Base(dst))
	}
	srcDir, err := tree.OpenDir(s.root, path.Dir(rel))
	if err != nil {
		return err
	}
	defer srcDir.Close()
	return s.cloneAt(srcDir, path.Base(rel), dir, filepath.Base(dst))
}

// makeWritable lets the store add and remove entries in the directory path.
// Directories in the base keep the project's modes, and a read-only one
// would otherwise stop merges. Their recorded modes live in the listings,
// not on disk.
func (s *Store) makeWritable(path string) error {
	// A whole-directory clone keeps a locked folder's lock flags, which stop
	// adding and removing entries whatever its mode.
	if err := s.unlock(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	info, err := s.lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o700 == 0o700 {
		return err
	}
	return s.files.Chmod(s.storeName(path), info.Mode().Perm()|0o700)
}

// unlock clears the lock flags on path that a whole-directory clone kept.
func (s *Store) unlock(path string) error {
	// The store directory itself is never a clone.
	if s.storeName(path) == "." {
		return nil
	}
	dir, err := s.open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return clone.UnlockAt(dir, filepath.Base(path))
}

// removeAll removes path and everything below it, including directories that
// were cloned read-only and files whose lock flags a whole-directory clone
// kept.
func (s *Store) removeAll(path string) error {
	name := s.storeName(path)
	err := s.files.RemoveAll(name)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// Directories that cannot be made writable make the final RemoveAll fail,
	// and that error is the one to report.
	_ = fs.WalkDir(s.files.FS(), name, func(entryName string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entryPath := filepath.Join(s.dir, entryName)
		_ = s.unlock(entryPath)
		if entry.IsDir() {
			_ = s.makeWritable(entryPath)
		}
		return nil
	})
	if err := s.makeWritable(filepath.Dir(path)); err != nil {
		return err
	}
	return s.files.RemoveAll(name)
}
