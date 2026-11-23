package store

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/tree"
)

// Source is the state of a path at a checkpoint, plus the stored copy that
// holds its content.
type Source struct {
	Entry tree.Entry
	Path  string
}

// State returns every path of the project as it was at checkpoint id. It
// starts from the base and applies each layer up to id.
func (s *Store) State(id int) (map[string]Source, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(list, func(cp Checkpoint) bool { return cp.ID == id }) {
		return nil, fmt.Errorf("no checkpoint %d", id)
	}
	base, err := s.BaseListing()
	if err != nil {
		return nil, err
	}
	state := make(map[string]Source, len(base))
	for rel, entry := range base {
		state[rel] = Source{Entry: entry, Path: filepath.Join(s.BaseDir(), filepath.FromSlash(rel))}
	}
	for _, cp := range list {
		if cp.ID > id {
			break
		}
		manifest, err := s.Manifest(cp.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(state, rel)
		}
		for rel, entry := range manifest.Changed {
			state[rel] = Source{Entry: entry, Path: s.LayerFile(cp.ID, rel)}
		}
	}
	return state, nil
}
