package store

import (
	"errors"
	"io/fs"
)

// lostRecord lists the paths in the base whose stored copy is gone, and the
// ones the next commit must store again from the project.
type lostRecord struct {
	Paths   []string `json:"paths"`
	Pending []string `json:"pending"`
}

// readLost returns the lost record, which is empty when no copy was lost.
func (s *Store) readLost() (lostRecord, error) {
	var record lostRecord
	if err := readJSON(s.lostPath(), &record); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return lostRecord{}, err
	}
	return record, nil
}

// setOf returns paths as a set.
func setOf(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, rel := range paths {
		set[rel] = true
	}
	return set
}

func (s *Store) writeLost(record lostRecord) error {
	return writeJSON(s.lostPath(), record)
}
