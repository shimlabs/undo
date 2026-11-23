package store

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// MarkerDir creates the directory where the watcher writes the marker files
// it waits for, and returns its path. It must be a real directory, so the
// markers never land outside the store.
func (s *Store) MarkerDir() (string, error) {
	path := s.markersDir()
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
	file, err := s.openFile(s.markerPath(name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	return file.Close()
}

// RemoveMarker removes the marker file name.
func (s *Store) RemoveMarker(name string) error {
	return s.remove(s.markerPath(name))
}

// WatcherFailure returns why the watcher last failed to start, and when.
func (s *Store) WatcherFailure() (reason string, failedAt time.Time, err error) {
	path := s.watcherFailurePath()
	info, err := s.lstat(path)
	if err != nil {
		return "", time.Time{}, err
	}
	data, err := s.readFile(path)
	return string(data), info.ModTime(), err
}

// SaveWatcherFailure records why the watcher failed to start.
func (s *Store) SaveWatcherFailure(reason string) error {
	return s.writeFileAtomic(s.watcherFailurePath(), []byte(reason))
}
