package watch

import (
	"path"
	"sync"

	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/tree"
)

// markUnreportedChanges marks dirty the names created or removed in each
// listed directory whose entry no longer matches the listing. Creating,
// removing or renaming anything in a directory changes the directory's
// times, so a change whose event has not arrived shows here. It runs before
// the events are applied, which update the entries of the directories they
// name and would hide a late change in one of them. A changed directory that
// cannot be read returns errNeedsFullComparison.
func (t *tracker) markUnreportedChanges(dirty map[string]bool) error {
	dirs := t.changedDirs()
	if len(dirs) == 0 {
		return nil
	}
	if t.children == nil {
		t.children = newChildIndex(t.listing)
	}
	parents := newParentDirs(t.root)
	defer parents.close()
	for _, dir := range dirs {
		markDirty(dirty, t.path(dir))
		names, err := readNames(parents, dir)
		if isGone(err) {
			continue
		}
		if err != nil {
			return errNeedsFullComparison
		}
		for name := range names {
			if _, isListed := t.listing[path.Join(dir, name)]; !isListed {
				markDirty(dirty, t.path(path.Join(dir, name)))
			}
		}
		for rel := range t.children[dir] {
			if !names[path.Base(rel)] {
				markDirty(dirty, t.path(rel))
			}
		}
	}
	return nil
}

// changedDirs returns the listed directories whose entry no longer matches
// the listing, or that cannot be described.
func (t *tracker) changedDirs() []string {
	var dirs []string
	for rel, entry := range t.listing {
		if entry.Kind == tree.Dir {
			dirs = append(dirs, rel)
		}
	}
	var (
		mu      sync.Mutex
		changed []string
	)
	// It never fails: a directory that cannot be described counts as changed.
	_ = parallel.For(len(dirs), func(i int) error {
		if entry, err := tree.Stat(t.path(dirs[i])); err != nil || entry != t.listing[dirs[i]] {
			mu.Lock()
			changed = append(changed, dirs[i])
			mu.Unlock()
		}
		return nil
	})
	return changed
}
