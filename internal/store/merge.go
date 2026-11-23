package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/shimlabs/undo/internal/tree"
)

type pendingMerge struct {
	Through int `json:"through"`
}

func (s *Store) mergePath() string {
	return filepath.Join(s.dir, "merge.json")
}

// merge folds every checkpoint up to through into the base and returns the
// bytes it freed. The merge is recorded first, so if it is interrupted, the
// next lock finishes it.
func (s *Store) merge(through int) (int64, error) {
	if err := writeJSON(s.mergePath(), pendingMerge{Through: through}); err != nil {
		return 0, err
	}
	return s.mergeRecorded()
}

func (s *Store) finishMerge() error {
	if _, err := os.Stat(s.mergePath()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_, err := s.mergeRecorded()
	return err
}

// mergeRecorded replays the layers named in the merge record from the
// original base listing. Replaying them in order is safe after an
// interruption, because each layer overwrites whatever an earlier attempt
// left behind.
func (s *Store) mergeRecorded() (int64, error) {
	var pending pendingMerge
	if err := readJSON(s.mergePath(), &pending); err != nil {
		return 0, err
	}
	base, err := s.BaseListing()
	if err != nil {
		return 0, err
	}
	checkpoints, err := s.List()
	if err != nil {
		return 0, err
	}
	var freed int64
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > pending.Through {
			break
		}
		layerFreed, err := s.mergeLayer(checkpoint.ID, base)
		if err != nil {
			return freed, err
		}
		freed += layerFreed
	}
	if err := writeGob(s.baseListingPath(), base); err != nil {
		return freed, err
	}
	remaining := slices.DeleteFunc(checkpoints, func(checkpoint Checkpoint) bool { return checkpoint.ID <= pending.Through })
	if err := s.writeIndex(remaining); err != nil {
		return freed, err
	}
	if err := s.removeLayersThrough(pending.Through); err != nil {
		return freed, err
	}
	return freed, os.Remove(s.mergePath())
}

func (s *Store) removeLayersThrough(through int) error {
	layers, err := os.ReadDir(filepath.Join(s.dir, "layers"))
	if err != nil {
		return err
	}
	for _, layer := range layers {
		if id, err := strconv.Atoi(layer.Name()); err == nil && id <= through {
			if err := removeAll(s.layerDir(id)); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeLayer applies layer id to the base directory and to base, and returns
// the bytes of base content it removed.
func (s *Store) mergeLayer(id int, base tree.Listing) (int64, error) {
	manifest, err := s.Manifest(id)
	if err != nil {
		return 0, err
	}
	var freed int64
	for _, rel := range manifest.Deleted {
		size, err := s.removeFromBase(rel)
		if err != nil {
			return freed, err
		}
		freed += size
		delete(base, rel)
	}
	paths := make([]string, 0, len(manifest.Changed))
	for rel := range manifest.Changed {
		paths = append(paths, rel)
	}
	slices.SortFunc(paths, func(a, b string) int { return tree.Depth(a) - tree.Depth(b) })
	for _, rel := range paths {
		entry := manifest.Changed[rel]
		var size int64
		switch entry.Kind {
		case tree.Dir:
			size, err = s.mergeDir(rel)
		case tree.File, tree.Symlink:
			size, err = s.mergeFile(id, rel)
		}
		if err != nil {
			return freed, err
		}
		freed += size
		base[rel] = entry
	}
	return freed, nil
}

// mergeDir makes sure rel is a directory in the base.
func (s *Store) mergeDir(rel string) (int64, error) {
	dst := s.basePath(rel)
	var freed int64
	if info, err := os.Lstat(dst); err == nil && !info.IsDir() {
		size, err := s.removeFromBase(rel)
		if err != nil {
			return 0, err
		}
		freed = size
	}
	if err := makeWritable(filepath.Dir(dst)); err != nil {
		return freed, err
	}
	return freed, os.MkdirAll(dst, 0o700)
}

// mergeFile replaces rel in the base with the copy in layer id.
func (s *Store) mergeFile(id int, rel string) (int64, error) {
	dst := s.basePath(rel)
	if err := makeWritable(filepath.Dir(dst)); err != nil {
		return 0, err
	}
	size, err := diskUsage(dst)
	if err != nil {
		return 0, err
	}
	if info, err := os.Lstat(dst); err == nil && info.IsDir() {
		if err := removeAll(dst); err != nil {
			return 0, err
		}
	}
	return size, s.Extract(rel, Source{Layer: id}, dst)
}

func (s *Store) removeFromBase(rel string) (int64, error) {
	path := s.basePath(rel)
	size, err := diskUsage(path)
	if err != nil {
		return 0, err
	}
	return size, removeAll(path)
}
