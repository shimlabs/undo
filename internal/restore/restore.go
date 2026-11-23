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

// Changes lists what a restore writes, removes and cannot restore, as paths
// relative to the project root.
type Changes struct {
	Written []string
	Removed []string
	// Skipped are sockets and devices, which a restore cannot recreate.
	Skipped []string
}

// Len returns the number of paths the restore writes or removes.
func (c Changes) Len() int {
	return len(c.Written) + len(c.Removed)
}

// Plan returns what restoring checkpoint id would change. It does not change
// anything.
func Plan(projectStore *store.Store, id int) (Changes, error) {
	project, unlock, err := lockAndCompare(projectStore, id)
	if err != nil {
		return Changes{}, err
	}
	unlock()
	return project.changes, nil
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
	project, unlock, err := lockAndCompare(projectStore, id)
	if err != nil {
		return Changes{}, err
	}
	isCovered, err := isCoveredBy(projectStore, safety.ID, project.current)
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
		if project, unlock, err = lockAndCompare(projectStore, id); err != nil {
			return Changes{}, err
		}
	}
	defer unlock()
	return project.changes, project.apply(projectStore)
}

// lockAndCompare takes the store's shared lock, so pruning cannot change the
// stored states during the restore, and plans the restore of checkpoint id.
func lockAndCompare(projectStore *store.Store, id int) (projectRestore, func(), error) {
	unlock, err := projectStore.RLock()
	if err != nil {
		return projectRestore{}, nil, err
	}
	project, err := compare(projectStore, id)
	if err != nil {
		unlock()
		return projectRestore{}, nil, err
	}
	return project, unlock, nil
}

// isCoveredBy reports whether the project still looks the way checkpoint id
// recorded it, so everything the restore overwrites can be brought back.
func isCoveredBy(projectStore *store.Store, id int, current tree.Listing) (bool, error) {
	saved, err := projectStore.ListingAt(id)
	if err != nil {
		return false, err
	}
	changed, deleted := tree.Changed(saved, current)
	return len(changed)+len(deleted) == 0, nil
}

// projectRestore is a planned restore: the target state, the project as it
// is, and the changes between them.
type projectRestore struct {
	root    string
	target  map[string]store.Source
	current tree.Listing
	changes Changes
}

// compare plans a restore of checkpoint id. Paths the current exclude rules
// exclude, and paths inside directories that cannot be read, are left out of
// both sides, so the restore never touches them.
func compare(projectStore *store.Store, id int) (projectRestore, error) {
	root := projectStore.Root()
	matcher, err := exclude.New(root)
	if err != nil {
		return projectRestore{}, err
	}
	target, err := projectStore.State(id)
	if err != nil {
		return projectRestore{}, err
	}
	current, skipped, err := tree.ScanWithSkipped(root, matcher)
	if err != nil {
		return projectRestore{}, err
	}
	for rel, source := range target {
		if matcher.Excludes(rel, source.Entry.Kind == tree.Dir) || isInside(rel, skipped.Unreadable) {
			delete(target, rel)
		}
	}
	targetListing := make(tree.Listing, len(target))
	for rel, source := range target {
		targetListing[rel] = source.Entry
	}
	written, removed := tree.Changed(current, targetListing)
	return projectRestore{root: root, target: target, current: current, changes: Changes{Written: written, Removed: removed}}, nil
}

func isInside(rel string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(dir string) bool {
		return strings.HasPrefix(rel, dir+"/")
	})
}

func (p *projectRestore) apply(projectStore *store.Store) error {
	if err := p.removeAll(p.removals()); err != nil {
		return err
	}
	skipped, err := p.writeAll(projectStore)
	p.changes.Skipped = skipped
	if err != nil {
		return err
	}
	return p.restoreDirModeAndTimes(slices.Concat(p.changes.Written, p.changes.Removed))
}

// removals returns the paths to delete before writing: paths that are not in
// the target, and paths whose kind changes, such as a file that must become a
// directory.
func (p *projectRestore) removals() []string {
	paths := slices.Clone(p.changes.Removed)
	for _, rel := range p.changes.Written {
		if entry, ok := p.current[rel]; ok && entry.Kind != p.target[rel].Entry.Kind {
			paths = append(paths, rel)
			delete(p.current, rel)
		}
	}
	return paths
}

// removeAll deletes paths, deepest first. A directory that still holds
// excluded files is left in place.
func (p *projectRestore) removeAll(paths []string) error {
	slices.SortFunc(paths, func(a, b string) int { return depth(b) - depth(a) })
	for _, rel := range paths {
		full := p.path(rel)
		if err := makeParentWritable(full); err != nil {
			return err
		}
		err := os.Remove(full)
		if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) {
			continue
		}
		return err
	}
	return nil
}

// writeAll writes the changed paths from the target, parents first. A
// directory that is missing from the project and whose whole content is
// unchanged since the base is cloned in one call. It returns the paths it
// cannot recreate.
func (p *projectRestore) writeAll(projectStore *store.Store) (skipped []string, err error) {
	paths := slices.Clone(p.changes.Written)
	slices.SortStableFunc(paths, func(a, b string) int { return depth(a) - depth(b) })
	clonable, err := p.clonableDirs(projectStore, paths)
	if err != nil {
		return nil, err
	}
	isWritten := map[string]bool{}
	for _, rel := range paths {
		if rel != "." && isWritten[path.Dir(rel)] {
			isWritten[rel] = true
			continue
		}
		source, full := p.target[rel], p.path(rel)
		if rel != "." {
			if err := makeParentWritable(full); err != nil {
				return skipped, err
			}
		}
		switch {
		case clonable[rel]:
			err = projectStore.ExtractBaseDir(rel, full)
			isWritten[rel] = true
		case source.Entry.Kind == tree.Dir:
			err = os.MkdirAll(full, 0o700)
		case source.Entry.Kind == tree.File || source.Entry.Kind == tree.Symlink:
			err = projectStore.Extract(rel, source, full)
		case source.Entry.Mode&fs.ModeNamedPipe != 0:
			err = syscall.Mkfifo(full, uint32(source.Entry.Mode.Perm()))
		default:
			skipped = append(skipped, rel)
		}
		if err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// clonableDirs returns the directories in paths that are missing from the
// project and can be cloned from the base as they are: everything inside them
// at the checkpoint comes from the base, and nothing was deleted from them
// since.
func (p *projectRestore) clonableDirs(projectStore *store.Store, paths []string) (map[string]bool, error) {
	candidates := map[string]bool{}
	for _, rel := range paths {
		if _, exists := p.current[rel]; !exists && rel != "." && p.target[rel].Entry.Kind == tree.Dir {
			candidates[rel] = true
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	base, err := projectStore.BaseListing()
	if err != nil {
		return nil, err
	}
	inTarget, inBase := map[string]int{}, map[string]int{}
	for rel, source := range p.target {
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if candidates[dir] {
				inTarget[dir]++
				if source.Layer != 0 {
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
	clonable := map[string]bool{}
	for dir, isCandidate := range candidates {
		if isCandidate && base[dir].Kind == tree.Dir && inTarget[dir] == inBase[dir] {
			clonable[dir] = true
		}
	}
	return clonable, nil
}

// restoreDirModeAndTimes sets the mode and times of every directory in paths,
// and of every directory that holds one of paths, deepest first. Writing or
// removing a path changes its directory's modification time, and the restore
// may have made a read-only directory writable, so this has to come last.
func (p *projectRestore) restoreDirModeAndTimes(paths []string) error {
	dirs := map[string]bool{}
	for _, rel := range paths {
		if p.target[rel].Entry.Kind == tree.Dir {
			dirs[rel] = true
		}
		if rel != "." {
			dirs[path.Dir(rel)] = true
		}
	}
	sorted := slices.Collect(maps.Keys(dirs))
	slices.SortFunc(sorted, func(a, b string) int { return depth(b) - depth(a) })
	for _, rel := range sorted {
		source, ok := p.target[rel]
		if !ok || source.Entry.Kind != tree.Dir {
			continue
		}
		if err := setModeAndTimes(p.path(rel), source.Entry); err != nil {
			return err
		}
	}
	return nil
}

func (p *projectRestore) path(rel string) string {
	return filepath.Join(p.root, filepath.FromSlash(rel))
}

// makeParentWritable lets the restore add or remove path in its directory.
// The directory's recorded mode is set again at the end.
func makeParentWritable(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o300 == 0o300 {
		return nil
	}
	return os.Chmod(parent, info.Mode().Perm()|0o300)
}

func setModeAndTimes(path string, entry tree.Entry) error {
	if err := os.Chmod(path, permissionBits(entry.Mode)); err != nil {
		return err
	}
	modTime := time.Unix(0, entry.ModTime)
	return os.Chtimes(path, modTime, modTime)
}

// permissionBits returns the part of mode that chmod sets.
func permissionBits(mode fs.FileMode) fs.FileMode {
	return mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}

func depth(rel string) int {
	if rel == "." {
		return -1
	}
	return strings.Count(rel, "/")
}
