package store

import (
	"errors"
	"io/fs"
)

// lostRecord lists the paths in the base whose stored copy is gone, and the
// ones the next commit must store again from the project.
type lostRecord struct {
	Paths   []string
	Pending []string
}

// readLost returns the lost record, which is empty when no copy was lost.
func (s *Store) readLost() (lostRecord, error) {
	var record lostRecord
	if err := s.readGob(s.lostPath(), &record); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return lostRecord{}, err
	}
	return record, nil
}

// writeLost replaces the lost record.
func (s *Store) writeLost(record lostRecord) error {
	return s.writeGob(s.lostPath(), record)
}
