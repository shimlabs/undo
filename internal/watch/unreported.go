package watch

import (
	"sync/atomic"

	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/tree"
)

// hasUnreportedChange reports whether a directory in the listing no longer
// matches it. FSEvents can deliver a change made before the marker after it,
// when it joins a later event for the same path; creating, removing or
// renaming anything in a directory changes the directory's times, so such a
// change shows here even before its event arrives.
func (t *tracker) hasUnreportedChange() bool {
	var dirs []string
	for rel, entry := range t.listing {
		if entry.Kind == tree.Dir {
			dirs = append(dirs, rel)
		}
	}
	var hasChange atomic.Bool
	// It never fails: a directory that cannot be described counts as changed.
	_ = parallel.For(len(dirs), func(i int) error {
		if hasChange.Load() {
			return nil
		}
		if entry, err := tree.Stat(t.path(dirs[i])); err != nil || entry != t.listing[dirs[i]] {
			hasChange.Store(true)
		}
		return nil
	})
	return hasChange.Load()
}
