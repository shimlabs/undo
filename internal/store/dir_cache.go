package store

import (
	"errors"
	"os"
	"sync"
)

// maxOpenDirs bounds how many directories a checkpoint, restore or merge
// holds open at once. Some systems allow only a few thousand descriptors.
const maxOpenDirs = 128

// dirCache keeps store directories open, so writing or reading many files
// in one directory walks the store root to it once. It holds at most
// maxOpenDirs directories open, closing those no caller is using to make
// room.
type dirCache struct {
	store *Store
	// prepare, if set, runs on a directory before it is first opened.
	prepare func(path string) error
	mu      sync.Mutex
	dirs    map[string]*cachedDir
}

type cachedDir struct {
	file *os.File
	// users is how many callers hold the directory and have not released it.
	users int
}

func (s *Store) newDirCache(prepare func(path string) error) *dirCache {
	return &dirCache{store: s, prepare: prepare, dirs: map[string]*cachedDir{}}
}

// open returns the open directory path in the store, opening it on first
// use, and a function to call once the caller is done with it.
func (c *dirCache) open(path string) (*os.File, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dir, isOpen := c.dirs[path]
	if !isOpen {
		var err error
		if dir, err = c.openUncached(path); err != nil {
			return nil, nil, err
		}
	}
	dir.users++
	return dir.file, func() { c.release(dir) }, nil
}

// openUncached opens path and adds it to the cache, first closing unused
// directories if the cache is full. The caller holds c.mu.
func (c *dirCache) openUncached(path string) (*cachedDir, error) {
	if len(c.dirs) >= maxOpenDirs {
		c.closeUnused()
	}
	if c.prepare != nil {
		if err := c.prepare(path); err != nil {
			return nil, err
		}
	}
	file, err := c.store.open(path)
	if err != nil {
		return nil, err
	}
	dir := &cachedDir{file: file}
	c.dirs[path] = dir
	return dir, nil
}

func (c *dirCache) release(dir *cachedDir) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dir.users--
}

// closeUnused closes the directories no caller is using. Callers work in
// parallel at most parallel.For's limit at a time, so this always makes room.
func (c *dirCache) closeUnused() {
	for path, dir := range c.dirs {
		if dir.users == 0 {
			dir.file.Close()
			delete(c.dirs, path)
		}
	}
}

// reset closes every open directory. A directory removed while open would
// otherwise take later writes with it, so no caller may hold one.
func (c *dirCache) reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	errs := make([]error, 0, len(c.dirs))
	for path, dir := range c.dirs {
		errs = append(errs, dir.file.Close())
		delete(c.dirs, path)
	}
	return errors.Join(errs...)
}
