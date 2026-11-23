package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/shimlabs/undo/internal/walk"
)

// Usage returns the bytes the store uses that are not shared with the
// project.
func (s *Store) Usage() (int64, error) {
	return diskUsage(s.dir)
}

func diskUsage(root string) (int64, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return 0, nil
		}
		return privateSize(root)
	}
	var total atomic.Int64
	err = walk.Dirs(root, func(dir string) ([]string, error) {
		children, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var subdirs []string
		for _, child := range children {
			path := filepath.Join(dir, child.Name())
			switch {
			case child.IsDir():
				subdirs = append(subdirs, path)
			case child.Type().IsRegular():
				size, err := privateSize(path)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				total.Add(size)
			}
		}
		return subdirs, nil
	})
	return total.Load(), err
}
