package store

import (
	"errors"
	"io/fs"

	"github.com/shimlabs/undo/internal/tree"
)

// restoreRecord is what the last restore left behind: the checkpoint it
// restored and the entries of the paths it wrote.
type restoreRecord struct {
	ID      int
	Entries tree.Listing
}

// SaveRestore records that the project was restored to checkpoint id and the
// entries the restore left. Restored files have new inodes and change times,
// so without this record, planning the same restore again would find every
// one of them changed.
func (s *Store) SaveRestore(id int, entries tree.Listing) error {
	return s.writeGob(s.restorePath(), restoreRecord{ID: id, Entries: entries})
}

// RestoredEntries returns the entries the last restore of checkpoint id left,
// or nil if the last restore was of another checkpoint.
func (s *Store) RestoredEntries(id int) (tree.Listing, error) {
	var record restoreRecord
	err := s.readGob(s.restorePath(), &record)
	if errors.Is(err, fs.ErrNotExist) || record.ID != id {
		return nil, nil
	}
	return record.Entries, err
}
