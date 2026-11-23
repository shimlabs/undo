package store

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

type pendingMerge struct {
	Through int `json:"through"`
}

// merge folds every checkpoint up to through into the base and returns the
// bytes it freed. The merge is recorded first, so if it is interrupted, the
// next lock finishes it.
func (s *Store) merge(through int) (int64, error) {
	// The usage record no longer matches once layers are merged.
	if err := s.remove(s.usagePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if err := s.writeJSON(s.mergePath(), pendingMerge{Through: through}); err != nil {
		return 0, err
	}
	return s.mergeRecorded()
}

// finishMerge completes a merge that was interrupted. The interruption can
// leave a half-written temporary file in the base, which the replay does not
// know about, so it is removed too.
func (s *Store) finishMerge() error {
	if _, err := s.lstat(s.mergePath()); errors.Is(err, fs.ErrNotExist) {
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
	if err := s.readJSON(s.mergePath(), &pending); err != nil {
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
	record, err := s.readLost()
	if err != nil {
		return 0, err
	}
	lost := toSet(record.Paths)
	var freed int64
	parents := tree.NewParentChecker(s.baseDir())
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > pending.Through {
			break
		}
		layerFreed, err := s.mergeLayer(checkpoint.ID, base, lost, parents)
		if err != nil {
			return freed, err
		}
		freed += layerFreed
	}
	// Listings, the watcher's included, still hold the lost paths as
	// stored, so the next commit stores them again from the project.
	toStoreAgain := toSet(record.Pending)
	for rel := range lost {
		toStoreAgain[rel] = true
	}
	record = lostRecord{Paths: slices.Sorted(maps.Keys(lost)), Pending: slices.Sorted(maps.Keys(toStoreAgain))}
	if err := s.writeLost(record); err != nil {
		return freed, err
	}
	if err := s.writeGob(s.baseListingPath(), base); err != nil {
		return freed, err
	}
	remaining := slices.DeleteFunc(checkpoints, func(checkpoint Checkpoint) bool { return checkpoint.ID <= pending.Through })
	if err := s.writeIndex(remaining); err != nil {
		return freed, err
	}
	if err := s.removeLayersThrough(pending.Through); err != nil {
		return freed, err
	}
	return freed, s.remove(s.mergePath())
}

func (s *Store) removeLayersThrough(through int) error {
	layers, err := s.readDir(s.layersDir())
	if err != nil {
		return err
	}
	for _, layer := range layers {
		if id, err := strconv.Atoi(layer.Name()); err == nil && id <= through {
			if err := s.removeAll(s.layerDir(id)); err != nil {
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
// Paths whose stored copy is gone stay in base and are added to lost; a path
// a later layer stores again leaves lost.
func (s *Store) mergeLayer(id int, base tree.Listing, lost map[string]bool, parents *tree.ParentChecker) (int64, error) {
	manifest, err := s.Manifest(id)
	if err != nil {
		return 0, err
	}
	m := &layerMerge{store: s, id: id, parents: parents, baseDirs: s.newDirCache(s.makeWritable), extractor: s.NewExtractor()}
	defer m.close()
	var freed int64
	for _, rel := range manifest.Deleted {
		size, err := m.removeFromBase(rel)
		if err != nil {
			return freed, err
		}
		freed += size
		delete(base, rel)
		delete(lost, rel)
	}
	paths := slices.Collect(maps.Keys(manifest.Changed))
	// The paths of one directory come one after another, so the open
	// directories stay under the cache's limit.
	tree.SortParentsFirst(paths)
	for _, rel := range paths {
		entry := manifest.Changed[rel]
		if err := m.makeBaseParents(rel); err != nil {
			return freed, err
		}
		var size int64
		switch entry.Kind {
		case tree.Other:
			// A named pipe or other special file has no stored copy; the
			// listing alone records it.
		case tree.Dir:
			size, err = m.mergeDir(rel)
		case tree.File, tree.Symlink:
			size, err = m.mergeFile(rel)
			if errors.Is(err, ErrStoredFileMissing) {
				// The merge goes on instead of stopping for good. The path
				// stays in the base listing, marked lost, so a restore never
				// deletes the file the project still has.
				s.LogError(fmt.Errorf("merging checkpoint %d: %w", id, err))
				if size, err = m.removeFromBase(rel); err != nil {
					return freed, err
				}
				freed += size
				base[rel] = entry
				lost[rel] = true
				continue
			}
		}
		if err != nil {
			return freed, err
		}
		freed += size
		base[rel] = entry
		delete(lost, rel)
	}
	return freed, nil
}

// layerMerge applies one layer to the base. It keeps the base directories it
// writes into open, made writable once, and closes them whenever it removes
// anything, since a removed directory must not take later writes.
type layerMerge struct {
	store     *Store
	id        int
	parents   *tree.ParentChecker
	baseDirs  *dirCache
	extractor *Extractor
}

func (m *layerMerge) close() {
	_ = m.baseDirs.reset()
	_ = m.extractor.Close()
}

func (m *layerMerge) removeAll(path string) error {
	if err := m.baseDirs.reset(); err != nil {
		return err
	}
	return m.store.removeAll(path)
}

// mergeDir makes sure rel is a directory in the base.
func (m *layerMerge) mergeDir(rel string) (int64, error) {
	dst := m.store.basePath(rel)
	var freed int64
	if info, err := m.store.lstat(dst); err == nil && !info.IsDir() {
		size, err := m.removeFromBase(rel)
		if err != nil {
			return 0, err
		}
		freed = size
	}
	if err := m.store.makeWritable(filepath.Dir(dst)); err != nil {
		return freed, err
	}
	return freed, m.store.mkdirAll(dst)
}

// mergeFile replaces rel in the base with the copy in the layer.
func (m *layerMerge) mergeFile(rel string) (int64, error) {
	dst := m.store.basePath(rel)
	size, err := diskUsage(dst)
	if err != nil {
		return 0, err
	}
	dir, release, err := m.baseDirs.open(filepath.Dir(dst))
	if err != nil {
		return 0, err
	}
	defer func() { release() }()
	name := filepath.Base(dst)
	var stat unix.Stat_t
	err = unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR:
		// The directory is removed, so the cache must not hold it open.
		release()
		release = func() {}
		if err := m.removeAll(dst); err != nil {
			return 0, err
		}
		m.parents.Forget(rel)
		if dir, release, err = m.baseDirs.open(filepath.Dir(dst)); err != nil {
			release = func() {}
			return 0, err
		}
	case err == nil:
		// A whole-directory clone of the base keeps lock flags.
		if err := clone.UnlockAt(dir, name); err != nil {
			return 0, err
		}
	case !errors.Is(err, unix.ENOENT):
		return 0, &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	return size, m.extractor.ExtractAt(rel, Source{Layer: m.id}, dir, name)
}

// removeFromBase removes rel from the base. A path below something that is
// not a directory is not in the base, so there is nothing to remove.
func (m *layerMerge) removeFromBase(rel string) (int64, error) {
	if err := m.parents.Check(rel); errors.Is(err, tree.ErrParentNotDir) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	basePath := m.store.basePath(rel)
	size, err := diskUsage(basePath)
	if err != nil {
		return 0, err
	}
	m.parents.Forget(rel)
	return size, m.removeAll(basePath)
}

// makeBaseParents makes every parent of rel in the base a real directory,
// replacing what a later layer put there, which that layer puts back when
// it is replayed.
func (m *layerMerge) makeBaseParents(rel string) error {
	err := m.parents.Check(rel)
	if !errors.Is(err, tree.ErrParentNotDir) {
		return err
	}
	var dirs []string
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	for _, dir := range slices.Backward(dirs) {
		info, err := m.store.lstat(m.store.basePath(dir))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := m.removeAll(m.store.basePath(dir)); err != nil {
				return err
			}
			m.parents.Forget(dir)
			break
		}
	}
	return m.store.mkdirAll(filepath.Dir(m.store.basePath(rel)))
}
