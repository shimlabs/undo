package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/clone"
)

// openFiles opens dir as the root every file operation of the store goes
// through. A symlink placed inside the store, or the store directory swapped
// for another after it was checked, cannot lead a read or write outside it.
func openFiles(dir string) (*os.Root, error) {
	files, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := files.Stat(".")
	if err == nil {
		var atPath fs.FileInfo
		if atPath, err = os.Lstat(dir); err == nil && !os.SameFile(opened, atPath) {
			err = fmt.Errorf("%s: %w", dir, ErrUnsafeStore)
		}
	}
	if err != nil {
		return nil, errors.Join(err, files.Close())
	}
	return files, nil
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

// openDir opens the directory path in the store.
func (s *Store) openDir(path string) (*os.File, error) {
	return s.files.Open(s.storeName(path))
}

func (s *Store) readDir(path string) ([]fs.DirEntry, error) {
	dir, err := s.openDir(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

// writeFileAtomic replaces path in one rename, after flushing its new
// content to disk.
func (s *Store) writeFileAtomic(path string, data []byte) error {
	name := s.storeName(path)
	tempName := filepath.Join(filepath.Dir(name), atomicfile.TempName(filepath.Base(name)))
	file, err := s.files.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = atomicfile.Sync(file)
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = s.files.Rename(tempName, name)
	}
	if err != nil {
		return errors.Join(err, s.files.Remove(tempName))
	}
	dir, err := s.files.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(atomicfile.Sync(dir), dir.Close())
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

// cloneFromProject copies the project path src to name in dir.
func (s *Store) cloneFromProject(src string, dir *os.File, name string) error {
	srcDir, err := os.Open(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer srcDir.Close()
	return s.cloneAt(srcDir, filepath.Base(src), dir, name)
}

// cloneIntoStore copies the project path src to the store path dst.
func (s *Store) cloneIntoStore(src, dst string) error {
	dir, err := s.openDir(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	return s.cloneFromProject(src, dir, filepath.Base(dst))
}

// makeWritable lets the store add and remove entries in the directory path.
// Directories in the base keep the project's modes, and a read-only one
// would otherwise stop merges. Their recorded modes live in the listings,
// not on disk.
func (s *Store) makeWritable(path string) error {
	info, err := s.lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o700 == 0o700 {
		return err
	}
	return s.files.Chmod(s.storeName(path), info.Mode().Perm()|0o700)
}

// unlock clears the lock flags on path that a whole-directory clone kept.
func (s *Store) unlock(path string) error {
	dir, err := s.openDir(filepath.Dir(path))
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
