package store

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"path"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/tree"
)

// Source is the state of a path at a checkpoint and where its content is
// stored: in the base when Layer is 0, or in that checkpoint's layer.
type Source struct {
	Entry tree.Entry
	Layer int
	// IsLost is set when the stored copy was gone when its checkpoint was
	// merged into the base. A restore leaves such a path alone.
	IsLost bool
}

// ErrMergePending means an earlier merge of old checkpoints did not finish,
// so the stored states are not consistent yet. The next checkpoint retries
// the merge, and the error log says why it failed.
var ErrMergePending = errors.New("an earlier cleanup of old checkpoints has not finished")

// State returns every path of the project as it was at checkpoint id. It
// starts from the base and applies each layer up to id.
func (s *Store) State(id int) (map[string]Source, error) {
	if _, err := s.lstat(s.mergePath()); err == nil {
		return nil, ErrMergePending
	}
	if _, err := s.Checkpoint(id); err != nil {
		return nil, err
	}
	checkpoints, err := s.List()
	if err != nil {
		return nil, err
	}
	base, err := s.baseListing()
	if err != nil {
		return nil, err
	}
	record, err := s.readLost()
	if err != nil {
		return nil, err
	}
	lost := toSet(record.Paths)
	state := make(map[string]Source, len(base))
	for rel, entry := range base {
		state[rel] = Source{Entry: entry, IsLost: lost[rel]}
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > id {
			break
		}
		manifest, err := s.Manifest(checkpoint.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(state, rel)
		}
		for rel, entry := range manifest.Changed {
			state[rel] = Source{Entry: entry, Layer: checkpoint.ID}
		}
	}
	return state, nil
}

// Manifest returns the manifest of checkpoint id.
func (s *Store) Manifest(id int) (Manifest, error) {
	var manifest Manifest
	if err := s.readGob(s.manifestPath(id), &manifest); err != nil {
		return Manifest{}, err
	}
	paths := slices.Concat(manifest.Deleted, manifest.Unstable, manifest.Unreadable, manifest.Excluded, slices.Collect(maps.Keys(manifest.Changed)))
	return manifest, checkLocal(slices.Values(paths))
}

// ErrPathOutsideProject means a stored path points outside the project.
// Undo never writes such a path, so the store was changed by someone else.
var ErrPathOutsideProject = errors.New("a stored path points outside the project")

// checkLocal rejects stored paths that are absolute or climb out of the
// project with "..", so restoring or merging cannot touch other files.
func checkLocal(paths iter.Seq[string]) error {
	for rel := range paths {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return fmt.Errorf("%q: %w", rel, ErrPathOutsideProject)
		}
	}
	return nil
}

// UnchangedDirs returns the directories among dirs whose whole content in
// state is exactly what the base holds: everything inside them comes from
// the base, nothing was deleted from them since, and none of them holds a
// named pipe or other special file, which the base may not keep. Such a
// directory can be written with one ExtractBaseDir.
// It returns none where a directory cannot be cloned in one call: on Linux and
// in copy mode.
func (s *Store) UnchangedDirs(state map[string]Source, dirs []string) (map[string]bool, error) {
	if !clone.CanCloneDirs || s.IsCopyMode() || len(dirs) == 0 {
		return nil, nil
	}
	base, err := s.baseListing()
	if err != nil {
		return nil, err
	}
	candidates := toSet(dirs)
	for rel, source := range state {
		if source.Layer != 0 || source.Entry.Kind == tree.Other || source.IsLost {
			for dir := range parentDirs(rel) {
				delete(candidates, dir)
			}
		}
	}
	inState := countInside(maps.Keys(state), candidates)
	inBase := countInside(maps.Keys(base), candidates)
	unchanged := map[string]bool{}
	for dir := range candidates {
		if base[dir].Kind == tree.Dir && inState[dir] == inBase[dir] {
			unchanged[dir] = true
		}
	}
	return unchanged, nil
}

// countInside returns, for each directory in dirs, how many of paths lie
// inside it at any depth.
func countInside(paths iter.Seq[string], dirs map[string]bool) map[string]int {
	counts := map[string]int{}
	for rel := range paths {
		for dir := range parentDirs(rel) {
			if dirs[dir] {
				counts[dir]++
			}
		}
	}
	return counts
}

// parentDirs yields the directories that hold rel, nearest first, leaving
// out the project root.
func parentDirs(rel string) iter.Seq[string] {
	return func(yield func(string) bool) {
		if rel == "." {
			return
		}
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if !yield(dir) {
				return
			}
		}
	}
}
