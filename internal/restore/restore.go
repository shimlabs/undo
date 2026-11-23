// Package restore puts a project back the way it was at a checkpoint.
package restore

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

// Changes lists the paths a restore writes and the paths it removes, relative
// to the project root.
type Changes struct {
	Write  []string
	Remove []string
}

// Plan returns what restoring checkpoint id would change. It does not change
// anything.
func Plan(s *store.Store, id int) (Changes, error) {
	unlock, err := s.RLock()
	if err != nil {
		return Changes{}, err
	}
	defer unlock()
	target, err := s.State(id)
	if err != nil {
		return Changes{}, err
	}
	changes, _, err := diff(s, target)
	return changes, err
}

// Apply makes the project match checkpoint id. It first takes a checkpoint of
// the current state with checkpoint, so the restore itself can be undone.
// Excluded paths are never touched.
func Apply(s *store.Store, id int, checkpoint store.Checkpointer) (Changes, error) {
	list, err := s.List()
	if err != nil {
		return Changes{}, err
	}
	if !slices.ContainsFunc(list, func(cp store.Checkpoint) bool { return cp.ID == id }) {
		return Changes{}, fmt.Errorf("no checkpoint %d", id)
	}
	if _, err := checkpoint(s, fmt.Sprintf("undo to %d", id), s.Root()); err != nil {
		return Changes{}, err
	}
	unlock, err := s.RLock()
	if err != nil {
		return Changes{}, err
	}
	defer unlock()
	target, err := s.State(id)
	if err != nil {
		return Changes{}, err
	}
	changes, current, err := diff(s, target)
	if err != nil {
		return Changes{}, err
	}

	removed := removals(changes, current, target)
	if err := removeAll(s.Root(), removed); err != nil {
		return changes, err
	}
	for _, rel := range removed {
		delete(current, rel)
	}
	if err := writeAll(s, changes.Write, current, target); err != nil {
		return changes, err
	}
	return changes, restoreDirTimes(s.Root(), slices.Concat(changes.Write, changes.Remove), target)
}

func diff(s *store.Store, target map[string]store.Source) (Changes, tree.Listing, error) {
	ex, err := exclude.New(s.Root())
	if err != nil {
		return Changes{}, nil, err
	}
	current, err := tree.Scan(s.Root(), ex)
	if err != nil {
		return Changes{}, nil, err
	}
	var changes Changes
	for rel, src := range target {
		if entry, ok := current[rel]; !ok || entry != src.Entry {
			changes.Write = append(changes.Write, rel)
		}
	}
	for rel := range current {
		if _, ok := target[rel]; !ok {
			changes.Remove = append(changes.Remove, rel)
		}
	}
	slices.Sort(changes.Write)
	slices.Sort(changes.Remove)
	return changes, current, nil
}

// removals returns the paths to delete before writing: paths that are not in
// the target, and paths whose kind changes, such as a file that must become a
// directory.
func removals(changes Changes, current tree.Listing, target map[string]store.Source) []string {
	paths := slices.Clone(changes.Remove)
	for _, rel := range changes.Write {
		if entry, ok := current[rel]; ok && entry.Kind != target[rel].Entry.Kind {
			paths = append(paths, rel)
		}
	}
	return paths
}

// removeAll deletes paths, deepest first. If a directory still contains
// excluded files, it is left in place.
func removeAll(root string, paths []string) error {
	slices.SortFunc(paths, func(a, b string) int { return depth(b) - depth(a) })
	for _, rel := range paths {
		err := os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
		if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) {
			continue
		}
		return err
	}
	return nil
}

// writeAll writes paths from target, parents first. A directory that is missing
// from the project and whose whole content is unchanged since the base is
// cloned in one call.
func writeAll(s *store.Store, paths []string, current tree.Listing, target map[string]store.Source) error {
	root := s.Root()
	paths = slices.Clone(paths)
	slices.SortStableFunc(paths, func(a, b string) int { return depth(a) - depth(b) })
	whole, err := wholeDirs(s, paths, current, target)
	if err != nil {
		return err
	}
	written := map[string]bool{}
	for _, rel := range paths {
		if written[path.Dir(rel)] && rel != "." {
			written[rel] = true
			continue
		}
		src := target[rel]
		full := filepath.Join(root, filepath.FromSlash(rel))
		_, exists := current[rel]
		switch src.Entry.Kind {
		case tree.Dir:
			if whole[rel] {
				if err := s.Clone(src.Path, full); err != nil {
					return err
				}
				written[rel] = true
				continue
			}
			if err := os.MkdirAll(full, 0o700); err != nil {
				return err
			}
		case tree.File, tree.Symlink:
			if exists {
				err = replace(s, src.Path, full)
			} else {
				err = s.Clone(src.Path, full)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// wholeDirs returns the directories in paths that are missing from the
// project and can be cloned from the base as they are: everything inside them
// at the checkpoint comes from the base, and nothing was deleted from them
// since.
func wholeDirs(s *store.Store, paths []string, current tree.Listing, target map[string]store.Source) (map[string]bool, error) {
	candidates := map[string]bool{}
	for _, rel := range paths {
		if _, exists := current[rel]; !exists && rel != "." && target[rel].Entry.Kind == tree.Dir {
			candidates[rel] = true
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	base, err := s.BaseListing()
	if err != nil {
		return nil, err
	}
	baseDir := s.BaseDir()
	inTarget, inBase := map[string]int{}, map[string]int{}
	for rel, src := range target {
		fromBase := src.Path == filepath.Join(baseDir, filepath.FromSlash(rel))
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if candidates[dir] {
				inTarget[dir]++
				if !fromBase {
					candidates[dir] = false
				}
			}
		}
	}
	for rel := range base {
		for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
			if candidates[dir] {
				inBase[dir]++
			}
		}
	}
	whole := map[string]bool{}
	for dir, ok := range candidates {
		if ok && base[dir].Kind == tree.Dir && inTarget[dir] == inBase[dir] {
			whole[dir] = true
		}
	}
	return whole, nil
}

// replace clones src next to dst and renames the clone into place. A clone
// cannot overwrite an existing path, so this is how dst gets replaced.
func replace(s *store.Store, src, dst string) error {
	tmp := dst + ".undo-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := s.Clone(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// restoreDirTimes sets the mode and times of every directory in paths, and of
// every directory that holds one of paths, deepest first. Writing or removing
// a path changes its directory's modification time, so this has to come last.
func restoreDirTimes(root string, paths []string, target map[string]store.Source) error {
	dirs := map[string]bool{}
	for _, rel := range paths {
		if target[rel].Entry.Kind == tree.Dir {
			dirs[rel] = true
		}
		if rel != "." {
			dirs[path.Dir(rel)] = true
		}
	}
	sorted := slices.Collect(maps.Keys(dirs))
	slices.SortFunc(sorted, func(a, b string) int { return depth(b) - depth(a) })
	for _, rel := range sorted {
		entry, ok := target[rel]
		if !ok || entry.Entry.Kind != tree.Dir {
			continue
		}
		if err := setMeta(filepath.Join(root, filepath.FromSlash(rel)), entry.Entry); err != nil {
			return err
		}
	}
	return nil
}

func setMeta(path string, entry tree.Entry) error {
	if err := os.Chmod(path, entry.Mode&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
		return err
	}
	mtime := time.Unix(0, entry.ModTime)
	return os.Chtimes(path, mtime, mtime)
}

func depth(rel string) int {
	if rel == "." {
		return -1
	}
	return strings.Count(rel, "/")
}
