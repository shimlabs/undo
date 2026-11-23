package store

import (
	"errors"
	"os"
	"sync"
)

// dirCache keeps store directories open, so writing or reading many files
// in one directory walks the store root to it once.
type dirCache struct {
	store *Store
	// prepare, if set, runs on a directory before it is first opened.
	prepare func(path string) error
	mu      sync.Mutex
	dirs    map[string]*os.File
}

func (s *Store) newDirCache(prepare func(path string) error) *dirCache {
	return &dirCache{store: s, prepare: prepare, dirs: map[string]*os.File{}}
}

// open returns the open directory path in the store, opening it on first
// use. The directory stays open until reset.
func (c *dirCache) open(path string) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dir, isOpen := c.dirs[path]; isOpen {
		return dir, nil
	}
	if c.prepare != nil {
		if err := c.prepare(path); err != nil {
			return nil, err
		}
	}
	dir, err := c.store.open(path)
	if err != nil {
		return nil, err
	}
	c.dirs[path] = dir
	return dir, nil
}

// reset closes every open directory. A directory removed while open would
// otherwise take later writes with it.
func (c *dirCache) reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	errs := make([]error, 0, len(c.dirs))
	for path, dir := range c.dirs {
		errs = append(errs, dir.Close())
		delete(c.dirs, path)
	}
	return errors.Join(errs...)
}
