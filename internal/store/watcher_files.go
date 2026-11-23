package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// MarkerDir creates the directory where the watcher writes the marker files
// it waits for, and returns its path. It must be a real directory, so the
// markers never land outside the store.
func (s *Store) MarkerDir() (string, error) {
	path := filepath.Join(s.dir, markersDirName)
	if err := s.mkdirAll(path); err != nil {
		return "", err
	}
	if info, err := s.lstat(path); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s: %w", path, ErrUnsafeStore)
	}
	return path, nil
}

// WriteMarker creates the empty marker file name in the marker directory.
func (s *Store) WriteMarker(name string) error {
	file, err := s.openFile(filepath.Join(s.dir, markersDirName, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

// RemoveMarker removes the marker file name.
func (s *Store) RemoveMarker(name string) error {
	return s.remove(filepath.Join(s.dir, markersDirName, name))
}

// WatcherFailure returns why the watcher last failed to start, and when.
func (s *Store) WatcherFailure() (reason string, failedAt time.Time, err error) {
	path := filepath.Join(s.dir, watcherFailureFile)
	info, err := s.lstat(path)
	if err != nil {
		return "", time.Time{}, err
	}
	data, err := s.readFile(path)
	return string(data), info.ModTime(), err
}

// SaveWatcherFailure records why the watcher failed to start.
func (s *Store) SaveWatcherFailure(reason string) error {
	return s.writeFileAtomic(filepath.Join(s.dir, watcherFailureFile), []byte(reason))
}

// WatcherPosition returns where a new watcher can resume the filesystem
// events, as the last watcher saved it.
func (s *Store) WatcherPosition() ([]byte, error) {
	return s.readFile(filepath.Join(s.dir, watcherPositionFile))
}

// SaveWatcherPosition records where a new watcher can resume.
func (s *Store) SaveWatcherPosition(position []byte) error {
	return s.writeFileAtomic(filepath.Join(s.dir, watcherPositionFile), position)
}
