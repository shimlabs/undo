package store

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/shimlabs/undo/internal/tree"
)

type pendingMerge struct {
	Through int `json:"through"`
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

// finishMerge completes a merge that was interrupted. The interruption can
// leave a half-written temporary file in the base, which the replay does not
// know about, so it is removed too.
func (s *Store) finishMerge() error {
	if _, err := os.Stat(s.mergePath()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := s.mergeRecorded(); err != nil {
		return err
	}
	listing, err := s.baseListing()
	if err != nil {
		return err
	}
	return s.removeUnlisted(listing)
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
	base, err := s.baseListing()
	if err != nil {
		return 0, err
	}
	checkpoints, err := s.List()
	if err != nil {
		return 0, err
	}
	var freed int64
	parents := tree.NewParentChecker(s.baseDir())
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > pending.Through {
			break
		}
		layerFreed, err := s.mergeLayer(checkpoint.ID, base, parents)
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
	layers, err := os.ReadDir(s.layersDir())
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
// the bytes of base content it removed. A replay after an interruption can
// find a later layer's symlink where this layer has a directory, so no path
// is touched through a parent that is not a real directory.
func (s *Store) mergeLayer(id int, base tree.Listing, parents *tree.ParentChecker) (int64, error) {
	manifest, err := s.Manifest(id)
	if err != nil {
		return 0, err
	}
	var freed int64
	for _, rel := range manifest.Deleted {
		size, err := s.removeFromBase(rel, parents)
		if err != nil {
			return freed, err
		}
		freed += size
		delete(base, rel)
	}
	paths := slices.Collect(maps.Keys(manifest.Changed))
	slices.SortFunc(paths, func(a, b string) int { return tree.Depth(a) - tree.Depth(b) })
	for _, rel := range paths {
		entry := manifest.Changed[rel]
		if err := s.makeBaseParents(rel, parents); err != nil {
			return freed, err
		}
		var size int64
		switch entry.Kind {
		case tree.Dir:
			size, err = s.mergeDir(rel, parents)
		case tree.File, tree.Symlink:
			size, err = s.mergeFile(id, rel, parents)
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
func (s *Store) mergeDir(rel string, parents *tree.ParentChecker) (int64, error) {
	dst := s.basePath(rel)
	var freed int64
	if info, err := os.Lstat(dst); err == nil && !info.IsDir() {
		size, err := s.removeFromBase(rel, parents)
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
func (s *Store) mergeFile(id int, rel string, parents *tree.ParentChecker) (int64, error) {
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
		parents.Forget(rel)
	}
	return size, s.Extract(rel, Source{Layer: id}, dst)
}

// removeFromBase removes rel from the base. A path below something that is
// not a directory is not in the base, so there is nothing to remove.
func (s *Store) removeFromBase(rel string, parents *tree.ParentChecker) (int64, error) {
	if err := parents.Check(rel); errors.Is(err, tree.ErrParentNotDir) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	path := s.basePath(rel)
	size, err := diskUsage(path)
	if err != nil {
		return 0, err
	}
	parents.Forget(rel)
	return size, removeAll(path)
}

// makeBaseParents makes every parent of rel in the base a real directory,
// replacing what a later layer put there, which that layer puts back when
// it is replayed.
func (s *Store) makeBaseParents(rel string, parents *tree.ParentChecker) error {
	err := parents.Check(rel)
	if !errors.Is(err, tree.ErrParentNotDir) {
		return err
	}
	var dirs []string
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	for _, dir := range slices.Backward(dirs) {
		info, err := os.Lstat(s.basePath(dir))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := removeAll(s.basePath(dir)); err != nil {
				return err
			}
			parents.Forget(dir)
			break
		}
	}
	return os.MkdirAll(filepath.Dir(s.basePath(rel)), 0o700)
}
