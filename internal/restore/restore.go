// Package restore puts a project back the way it was at a checkpoint.
package restore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/clone"
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

type source struct {
	entry tree.Entry
	path  string
}

// Plan returns what restoring checkpoint id would change. It does not change
// anything.
func Plan(s *store.Store, id int) (Changes, error) {
	target, err := targetState(s, id)
	if err != nil {
		return Changes{}, err
	}
	changes, _, err := diff(s, target)
	return changes, err
}

// Apply makes the project match checkpoint id. It first takes a checkpoint of
// the current state, so the restore itself can be undone. Excluded paths are
// never touched.
func Apply(s *store.Store, id int) (Changes, error) {
	if _, err := targetState(s, id); err != nil {
		return Changes{}, err
	}
	if _, err := s.Take(fmt.Sprintf("undo to %d", id), s.Root()); err != nil {
		return Changes{}, err
	}
	target, err := targetState(s, id)
	if err != nil {
		return Changes{}, err
	}
	changes, current, err := diff(s, target)
	if err != nil {
		return Changes{}, err
	}

	if err := removeAll(s.Root(), removals(changes, current, target)); err != nil {
		return changes, err
	}
	if err := writeAll(s.Root(), changes.Write, target); err != nil {
		return changes, err
	}
	return changes, restoreDirTimes(s.Root(), changes.Write, target)
}

// targetState applies the layers up to id on top of the base.
func targetState(s *store.Store, id int) (map[string]source, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(list, func(cp store.Checkpoint) bool { return cp.ID == id }) {
		return nil, fmt.Errorf("no checkpoint %d", id)
	}
	base, err := s.BaseListing()
	if err != nil {
		return nil, err
	}
	target := make(map[string]source, len(base))
	for rel, entry := range base {
		target[rel] = source{entry: entry, path: filepath.Join(s.BaseDir(), filepath.FromSlash(rel))}
	}
	for _, cp := range list {
		if cp.ID > id {
			break
		}
		manifest, err := s.Layer(cp.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(target, rel)
		}
		for rel, entry := range manifest.Changed {
			target[rel] = source{entry: entry, path: s.LayerFile(cp.ID, rel)}
		}
	}
	return target, nil
}

func diff(s *store.Store, target map[string]source) (Changes, tree.Listing, error) {
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
		if entry, ok := current[rel]; !ok || entry != src.entry {
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
func removals(changes Changes, current tree.Listing, target map[string]source) []string {
	paths := slices.Clone(changes.Remove)
	for _, rel := range changes.Write {
		if entry, ok := current[rel]; ok && entry.Kind != target[rel].entry.Kind {
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

func writeAll(root string, paths []string, target map[string]source) error {
	paths = slices.Clone(paths)
	slices.SortStableFunc(paths, func(a, b string) int { return depth(a) - depth(b) })
	for _, rel := range paths {
		src := target[rel]
		full := filepath.Join(root, filepath.FromSlash(rel))
		switch src.entry.Kind {
		case tree.Dir:
			if err := os.MkdirAll(full, 0o700); err != nil {
				return err
			}
		case tree.File, tree.Symlink:
			if err := replace(src.path, full); err != nil {
				return err
			}
			if src.entry.Kind == tree.File {
				if err := setMeta(full, src.entry); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// replace clones src next to dst and renames the clone into place. A clone
// cannot overwrite an existing path, so this is how dst gets replaced.
func replace(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".undo-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := clone.File(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// restoreDirTimes sets directory modes and times at the end, deepest first.
// Writing into a directory changes its modification time, so this has to come
// last.
func restoreDirTimes(root string, paths []string, target map[string]source) error {
	dirs := slices.DeleteFunc(slices.Clone(paths), func(rel string) bool { return target[rel].entry.Kind != tree.Dir })
	slices.SortFunc(dirs, func(a, b string) int { return depth(b) - depth(a) })
	for _, rel := range dirs {
		if err := setMeta(filepath.Join(root, filepath.FromSlash(rel)), target[rel].entry); err != nil {
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
