// Package restore puts a project back the way it was at a checkpoint.
package restore

import (
	"fmt"
	"slices"

	"github.com/shimlabs/undo/internal/store"
)

// Changes lists what a restore writes, removes and leaves alone, as paths
// relative to the project root.
type Changes struct {
	Written []string
	Removed []string
	// Skipped are sockets and devices, which a restore cannot recreate.
	Skipped []string
	// Kept are paths the restore leaves as they are: files undo cannot read
	// or that are locked, directories that hold such paths, and existing
	// paths that the exclude rules leave out now but not at the checkpoint.
	Kept []string
	// KeepReasons says why each kept path was kept.
	KeepReasons map[string]KeepReason
	// Dirs marks the written and removed paths that are directories.
	Dirs map[string]bool
	// Unstable are files that kept changing while the checkpoint saved them,
	// so their restored content may be a mix of two writes.
	Unstable []string
	// Lost are files whose stored copy is gone, as the newest checkpoint's
	// copies can be after a power cut.
	Lost []string
}

// KeepReason says why a restore left a path as it is.
type KeepReason int

const (
	// KeptUnreadable means undo cannot read the path now, so the safety
	// checkpoint could not save it.
	KeptUnreadable KeepReason = iota + 1
	// KeptLocked means the path, or the directory that holds it, is locked,
	// as Finder's Locked checkbox does.
	KeptLocked
	// KeptExcluded means today's exclude rules leave the path out, but the
	// checkpoint's did not.
	KeptExcluded
	// KeptOccupied means something the restore must not touch stands where
	// the checkpoint had another kind of entry, such as a directory holding
	// excluded files where a file was.
	KeptOccupied
)

// RestoredPaths returns the written paths the restore wrote: those whose
// stored copy was lost and the sockets and devices it skipped hold what the
// project had, not what the checkpoint did.
func (c Changes) RestoredPaths() []string {
	notWritten := toSet(slices.Concat(c.Lost, c.Skipped))
	return slices.DeleteFunc(slices.Clone(c.Written), func(rel string) bool { return notWritten[rel] })
}

func toSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, rel := range paths {
		set[rel] = true
	}
	return set
}

// Plan returns what restoring checkpoint id would change. It does not change
// anything.
func Plan(projectStore *store.Store, id int) (Changes, error) {
	plan, unlock, err := lockAndPlan(projectStore, id)
	if err != nil {
		return Changes{}, err
	}
	unlock()
	return plan.changes, nil
}

// Apply makes the project match checkpoint id. It first takes a checkpoint of
// the current state with checkpoint, so the restore itself can be undone.
// Paths that are excluded or unreadable now are never touched.
func Apply(projectStore *store.Store, id int, checkpoint store.Checkpointer) (Changes, error) {
	if _, err := projectStore.Checkpoint(id); err != nil {
		return Changes{}, err
	}
	options := store.CheckpointOptions{Label: fmt.Sprintf("undo to %d", id), Dir: projectStore.Root()}
	safety, err := checkpoint(projectStore, options)
	if err != nil {
		return Changes{}, err
	}
	plan, unlock, err := lockAndPlan(projectStore, id)
	if err != nil {
		return Changes{}, err
	}
	isCovered, err := isCoveredBy(projectStore, safety.ID, plan.scanned)
	if err != nil {
		unlock()
		return Changes{}, err
	}
	if !isCovered {
		// The watcher missed a change, so the safety checkpoint does not hold
		// everything the restore would overwrite. A full scan does.
		unlock()
		if _, err := projectStore.Take(options); err != nil {
			return Changes{}, err
		}
		if plan, unlock, err = lockAndPlan(projectStore, id); err != nil {
			return Changes{}, err
		}
	}
	defer unlock()
	return plan.changes, plan.apply(projectStore, id)
}
