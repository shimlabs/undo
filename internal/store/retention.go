package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shimlabs/undo/internal/tree"
	"github.com/shimlabs/undo/internal/walk"
)

// Limits bound how much history a store keeps.
type Limits struct {
	MaxAge   time.Duration
	MaxCount int
	MaxBytes int64
}

// DefaultLimits keeps a week of checkpoints, at most 2,000 of them, using at
// most 5 GB of disk that is not shared with the project.
var DefaultLimits = Limits{MaxAge: 7 * 24 * time.Hour, MaxCount: 2000, MaxBytes: 5_000_000_000}

// Prune merges the oldest checkpoints into the base until the store is within
// limits. The latest checkpoint is always kept. It returns how many
// checkpoints were merged. The store is locked only while merging, so
// measuring disk use does not hold up new checkpoints.
func (s *Store) Prune(now time.Time, limits Limits) (int, error) {
	count, err := s.mergeExpired(now, limits)
	if err != nil {
		return count, err
	}
	usage, err := s.Usage()
	if err != nil || usage <= limits.MaxBytes {
		return count, err
	}
	bySize, err := s.mergeUntilUnder(limits.MaxBytes, usage)
	return count + bySize, err
}

func (s *Store) mergeExpired(now time.Time, limits Limits) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	list, err := s.List()
	if err != nil {
		return 0, err
	}
	count := 0
	for count < len(list)-1 && (now.Sub(list[count].Time) > limits.MaxAge || len(list)-count > limits.MaxCount) {
		count++
	}
	if count == 0 {
		return 0, nil
	}
	_, err = s.merge(list[count-1].ID)
	return count, err
}

// mergeUntilUnder merges one checkpoint at a time and subtracts the bytes each
// merge frees from usage, since measuring the whole store after every merge
// would be slow.
func (s *Store) mergeUntilUnder(maxBytes, usage int64) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	list, err := s.List()
	if err != nil {
		return 0, err
	}
	count := 0
	for ; usage > maxBytes && count < len(list)-1; count++ {
		freed, err := s.merge(list[count].ID)
		if err != nil {
			return count, err
		}
		usage -= freed
	}
	return count, nil
}

// Usage returns the bytes the store uses that are not shared with the
// project.
func (s *Store) Usage() (int64, error) {
	return privateSizeAll(s.dir)
}

func privateSizeAll(root string) (int64, error) {
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
	err = walk.Dirs(root, walk.Workers, func(dir string) ([]string, error) {
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
	_, err := s.mergeRecorded()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
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
	list, err := s.List()
	if err != nil {
		return 0, err
	}
	var freed int64
	for _, cp := range list {
		if cp.ID > pending.Through {
			break
		}
		layerFreed, err := s.mergeLayer(cp.ID, base)
		if err != nil {
			return freed, err
		}
		freed += layerFreed
	}
	if err := s.saveGob("base.gob", base); err != nil {
		return freed, err
	}
	remaining := slices.DeleteFunc(list, func(cp Checkpoint) bool { return cp.ID <= pending.Through })
	if err := writeJSON(filepath.Join(s.dir, "index.json"), remaining); err != nil {
		return freed, err
	}
	layers, err := os.ReadDir(filepath.Join(s.dir, "layers"))
	if err != nil {
		return freed, err
	}
	for _, layer := range layers {
		if id, err := strconv.Atoi(layer.Name()); err == nil && id <= pending.Through {
			if err := os.RemoveAll(s.layerDir(id)); err != nil {
				return freed, err
			}
		}
	}
	return freed, os.Remove(s.mergePath())
}

// mergeLayer applies layer id to the base directory and to base, and returns
// the bytes of base content it removed.
func (s *Store) mergeLayer(id int, base tree.Listing) (int64, error) {
	manifest, err := s.Layer(id)
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
	slices.SortFunc(paths, func(a, b string) int { return strings.Count(a, "/") - strings.Count(b, "/") })
	for _, rel := range paths {
		entry := manifest.Changed[rel]
		dst := s.basePath(rel)
		switch entry.Kind {
		case tree.Dir:
			if info, err := os.Lstat(dst); err == nil && !info.IsDir() {
				size, err := s.removeFromBase(rel)
				if err != nil {
					return freed, err
				}
				freed += size
			}
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return freed, err
			}
		case tree.File, tree.Symlink:
			tmp := dst + ".undo-tmp"
			if err := os.RemoveAll(tmp); err != nil {
				return freed, err
			}
			if err := s.Clone(s.LayerFile(id, rel), tmp); err != nil {
				return freed, err
			}
			size, err := s.removeFromBase(rel)
			if err != nil {
				return freed, err
			}
			freed += size
			if err := os.Rename(tmp, dst); err != nil {
				return freed, err
			}
		}
		base[rel] = entry
	}
	return freed, nil
}

func (s *Store) removeFromBase(rel string) (int64, error) {
	path := s.basePath(rel)
	size, err := privateSizeAll(path)
	if err != nil {
		return 0, err
	}
	return size, os.RemoveAll(path)
}

func (s *Store) basePath(rel string) string {
	if rel == "." {
		return s.BaseDir()
	}
	return filepath.Join(s.BaseDir(), filepath.FromSlash(rel))
}
