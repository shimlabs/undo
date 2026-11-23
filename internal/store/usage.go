package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/shimlabs/undo/internal/tree"
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

// blockSize is what a file takes on disk at least. Sizes are rounded up to
// it, so estimates stay above the real use.
const blockSize = 4096

// storedBytes returns at most the disk space the copies of entries take.
func storedBytes(entries map[string]tree.Entry) int64 {
	var total int64
	for _, entry := range entries {
		total += blockSize
		if entry.Kind == tree.File {
			total += (entry.Size + blockSize - 1) / blockSize * blockSize
		}
	}
	return total
}

// usageBound returns a quick upper bound of Usage: everything the base lists
// and every checkpoint stored, as if none of it were shared, plus the
// store's own files.
func (s *Store) usageBound() (int64, error) {
	base, err := s.baseListing()
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	bound := storedBytes(base)
	checkpoints, err := s.List()
	if err != nil {
		return 0, err
	}
	for _, checkpoint := range checkpoints {
		bound += checkpoint.StoredBytes
	}
	for _, path := range []string{s.indexPath(), s.baseListingPath(), s.listingPath(), s.restorePath(), s.logPath()} {
		if info, err := s.lstat(path); err == nil {
			bound += info.Size() + blockSize
		}
	}
	return bound, nil
}

// usageRecord is the last measured Usage and the latest checkpoint then.
type usageRecord struct {
	Bytes    int64 `json:"bytes"`
	LatestID int   `json:"latest_id"`
}

// saveUsage records usage, measured while the store held the checkpoints
// measured, so UsageEstimate need not measure the store again. A merge since
// the measurement makes it stale, and then nothing is recorded.
func (s *Store) saveUsage(usage int64, measured []Checkpoint) error {
	if len(measured) == 0 {
		return nil
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	checkpoints, err := s.List()
	if err != nil || len(checkpoints) == 0 || checkpoints[0].ID != measured[0].ID {
		return err
	}
	return s.writeJSON(s.usagePath(), usageRecord{Bytes: usage, LatestID: measured[len(measured)-1].ID})
}

// UsageEstimate returns about the bytes the store uses that are not shared
// with the project. It measures the store once, which is slow for a large
// store, and then adds what each later checkpoint stored. Merging measures
// it again.
func (s *Store) UsageEstimate() (int64, error) {
	unlock, err := s.RLock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	checkpoints, err := s.List()
	if err != nil {
		return 0, err
	}
	var record usageRecord
	if err := s.readJSON(s.usagePath(), &record); err == nil {
		estimate := record.Bytes
		for _, checkpoint := range checkpoints {
			if checkpoint.ID > record.LatestID {
				estimate += checkpoint.StoredBytes
			}
		}
		return estimate, nil
	}
	usage, err := s.Usage()
	if err != nil {
		return 0, err
	}
	record = usageRecord{Bytes: usage}
	if len(checkpoints) > 0 {
		record.LatestID = checkpoints[len(checkpoints)-1].ID
	}
	return usage, s.writeJSON(s.usagePath(), record)
}
