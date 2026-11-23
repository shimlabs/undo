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

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

// Changes lists what a restore writes, removes and leaves alone, as paths
// relative to the project root.
type Changes struct {
	Written []string
	Removed []string
	// Skipped are sockets and devices, which a restore cannot recreate.
	Skipped []string
	// Kept are paths the restore leaves as they are because they were never
	// saved: files undo cannot read, and existing paths that the exclude
	// rules leave out now but not at the checkpoint.
	Kept []string
	// Unstable are files that kept changing while the checkpoint saved them,
	// so their restored content may be a mix of two writes.
	Unstable []string
}

// Len returns the number of paths the restore writes or removes.
func (c Changes) Len() int {
	return len(c.Written) + len(c.Removed)
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
	isCovered, err := isCoveredBy(projectStore, safety.ID, plan.current)
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

// lockAndPlan takes the store's shared lock, so pruning cannot change the
// stored states during the restore, and plans the restore of checkpoint id.
func lockAndPlan(projectStore *store.Store, id int) (restorePlan, func(), error) {
	unlock, err := projectStore.RLock()
	if err != nil {
		return restorePlan{}, nil, err
	}
	plan, err := planRestore(projectStore, id)
	if err != nil {
		unlock()
		return restorePlan{}, nil, err
	}
	return plan, unlock, nil
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

// restorePlan is a planned restore: the target state, the project as it
// is, and the changes between them.
type restorePlan struct {
	// projectRoot is open while the plan is applied.
	projectRoot *os.Root
	root        string
	target      map[string]store.Source
	current     tree.Listing
	changes     Changes
}

// planRestore plans a restore of checkpoint id. It never touches a path the
// checkpoint did not save: paths its exclude rules left out, paths in
// directories that could not be read then or now, and files that cannot be
// read now, which the safety checkpoint cannot save either. A path that only
// today's rules exclude is written back if it is missing, but never
// overwritten, and nothing inside it is touched.
func planRestore(projectStore *store.Store, id int) (restorePlan, error) {
	root := projectStore.Root()
	matcherNow, err := exclude.New(root)
	if err != nil {
		return restorePlan{}, err
	}
	manifest, err := projectStore.Manifest(id)
	if err != nil {
		return restorePlan{}, err
	}
	matcherThen := matcherNow
	if manifest.ExcludePatterns != nil {
		matcherThen = exclude.FromPatterns(manifest.ExcludePatterns)
	}
	target, err := projectStore.State(id)
	if err != nil {
		return restorePlan{}, err
	}
	current, skipped, err := tree.Scan(root, matcherNow)
	if err != nil {
		return restorePlan{}, err
	}
	plan := restorePlan{root: root, target: target, current: current}
	kept := plan.dropUnsaved(matcherThen, matcherNow, slices.Concat(manifest.Unreadable, skipped.Unreadable))
	written, removed := tree.Changed(current, plan.targetListing())
	// The safety checkpoint could not save a file undo cannot read, so the
	// restore must not change it either.
	// A directory that holds paths the restore must not touch cannot be
	// replaced by a file either.
	unreadable := plan.unreadableFiles(slices.Concat(written, removed))
	blocked := plan.dirsHoldingUntracked(written)
	if left := slices.Concat(unreadable, blocked); len(left) > 0 {
		plan.drop(left)
		kept = append(kept, left...)
		written, removed = tree.Changed(current, plan.targetListing())
	}
	written = slices.DeleteFunc(written, isInsideAny(kept))
	removed = slices.DeleteFunc(removed, isInsideAny(kept))
	restored, err := projectStore.RestoredEntries(id)
	if err != nil {
		return restorePlan{}, err
	}
	written = slices.DeleteFunc(written, func(rel string) bool {
		entry, isRestored := restored[rel]
		return isRestored && current[rel] == entry
	})
	slices.Sort(kept)
	plan.changes = Changes{Written: written, Removed: removed, Kept: kept, Unstable: manifest.Unstable}
	return plan, nil
}

// dropUnsaved removes from the plan every path the checkpoint did not save:
// paths its exclude rules left out and paths in unreadable directories. It
// also drops existing paths that only today's rules exclude, and returns them.
func (p *restorePlan) dropUnsaved(matcherThen, matcherNow *exclude.Matcher, unreadable []string) (kept []string) {
	isUnsaved := func(rel string, isDir bool) bool {
		return matcherThen.Excludes(rel, isDir) || isAtOrInside(rel, unreadable)
	}
	for rel, entry := range p.current {
		if isUnsaved(rel, entry.Kind == tree.Dir) {
			delete(p.current, rel)
		}
	}
	for rel, source := range p.target {
		isDir := source.Entry.Kind == tree.Dir
		switch {
		case isUnsaved(rel, isDir):
			delete(p.target, rel)
		case matcherNow.Excludes(rel, isDir) && exists(p.path(rel)):
			delete(p.target, rel)
			kept = append(kept, rel)
		}
	}
	return kept
}

// unreadableFiles returns the files among paths that exist now and cannot be
// read.
func (p *restorePlan) unreadableFiles(paths []string) []string {
	var unreadable []string
	for _, rel := range paths {
		if entry, isCurrent := p.current[rel]; isCurrent && entry.Kind == tree.File && !isReadable(p.path(rel)) {
			unreadable = append(unreadable, rel)
		}
	}
	return unreadable
}

// dirsHoldingUntracked returns the directories among paths that must become
// files or symlinks but hold something the plan does not track, such as
// excluded or unreadable paths.
func (p *restorePlan) dirsHoldingUntracked(paths []string) []string {
	var blocked []string
	for _, rel := range paths {
		if p.current[rel].Kind != tree.Dir || p.target[rel].Entry.Kind == tree.Dir {
			continue
		}
		entries, err := os.ReadDir(p.path(rel))
		if err != nil {
			blocked = append(blocked, rel)
			continue
		}
		if slices.ContainsFunc(entries, func(entry fs.DirEntry) bool {
			_, isTracked := p.current[rel+"/"+entry.Name()]
			return !isTracked
		}) {
			blocked = append(blocked, rel)
		}
	}
	return blocked
}

// drop removes paths from the plan, so the restore leaves them as they are.
func (p *restorePlan) drop(paths []string) {
	for _, rel := range paths {
		delete(p.current, rel)
		delete(p.target, rel)
	}
}

func (p *restorePlan) targetListing() tree.Listing {
	listing := make(tree.Listing, len(p.target))
	for rel, source := range p.target {
		listing[rel] = source.Entry
	}
	return listing
}

// isInsideAny returns a function that reports whether a path is inside one of
// dirs, which the restore leaves as they are.
func isInsideAny(dirs []string) func(rel string) bool {
	isKept := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		isKept[dir] = true
	}
	return func(rel string) bool {
		for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
			if isKept[dir] {
				return true
			}
		}
		return false
	}
}

// isAtOrInside reports whether rel is one of dirs or inside one of them.
func isAtOrInside(rel string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(dir string) bool {
		return rel == dir || strings.HasPrefix(rel, dir+"/")
	})
}

func isReadable(fullPath string) bool {
	return unix.Access(fullPath, unix.R_OK) == nil
}

func exists(fullPath string) bool {
	_, err := os.Lstat(fullPath)
	return err == nil
}

// apply makes the changes. Every write, removal and mode change goes through
// a root opened at the project, so a directory swapped for a symlink while
// the restore runs cannot lead it outside the project.
func (p *restorePlan) apply(projectStore *store.Store, id int) error {
	// The project folder itself may have been deleted. Its mode and times
	// are set at the end, with the other directories.
	if err := os.MkdirAll(p.root, 0o700); err != nil {
		return err
	}
	projectRoot, err := os.OpenRoot(p.root)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	p.projectRoot = projectRoot
	if err := p.removeAll(p.removals()); err != nil {
		return err
	}
	skipped, err := p.writeAll(projectStore)
	p.changes.Skipped = skipped
	if err != nil {
		return err
	}
	dirs, err := p.restoreDirModeAndTimes(slices.Concat(p.changes.Written, p.changes.Removed))
	if err != nil {
		return err
	}
	return projectStore.SaveRestore(id, p.restoredEntries(slices.Concat(p.changes.Written, dirs)))
}

// restoredEntries returns the current entries of paths, as the restore left
// them.
func (p *restorePlan) restoredEntries(paths []string) tree.Listing {
	entries := make(tree.Listing, len(paths))
	for _, rel := range paths {
		if entry, err := tree.Stat(p.path(rel)); err == nil {
			entries[rel] = entry
		}
	}
	return entries
}

// removals returns the paths to delete before writing: paths that are not in
// the target, and paths whose kind changes, such as a file that must become a
// directory.
func (p *restorePlan) removals() []string {
	paths := slices.Clone(p.changes.Removed)
	for _, rel := range p.changes.Written {
		// A named pipe cannot be rewritten in place, so a changed one is
		// removed and made again.
		if entry, ok := p.current[rel]; ok && (entry.Kind != p.target[rel].Entry.Kind || entry.Kind == tree.Other) {
			paths = append(paths, rel)
			delete(p.current, rel)
		}
	}
	return paths
}

// removeAll deletes paths, deepest first. A directory that still holds
// excluded files is left in place.
func (p *restorePlan) removeAll(paths []string) error {
	slices.SortFunc(paths, func(a, b string) int { return tree.Depth(b) - tree.Depth(a) })
	parents := tree.NewParentChecker(p.root)
	for _, rel := range paths {
		if err := parents.Check(rel); err != nil {
			return err
		}
		if err := p.makeParentWritable(rel); err != nil {
			return err
		}
		err := p.projectRoot.Remove(filepath.FromSlash(rel))
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
func (p *restorePlan) writeAll(projectStore *store.Store) (skipped []string, err error) {
	paths := slices.Clone(p.changes.Written)
	slices.SortStableFunc(paths, func(a, b string) int { return tree.Depth(a) - tree.Depth(b) })
	clonable, err := p.clonableDirs(projectStore, paths)
	if err != nil {
		return nil, err
	}
	isCloned := map[string]bool{}
	var files []string
	parents := tree.NewParentChecker(p.root)
	for _, rel := range paths {
		if rel != "." && isCloned[path.Dir(rel)] {
			isCloned[rel] = true
			continue
		}
		if err := parents.Check(rel); err != nil {
			return skipped, err
		}
		source := p.target[rel]
		if rel != "." {
			if err := p.makeParentWritable(rel); err != nil {
				return skipped, err
			}
		}
		switch {
		case clonable[rel]:
			err = p.inParent(rel, func(dir *os.File, name string) error {
				return projectStore.ExtractBaseDir(rel, dir, name)
			})
			isCloned[rel] = true
		case source.Entry.Kind == tree.Dir:
			err = p.projectRoot.MkdirAll(filepath.FromSlash(rel), 0o700)
		case source.Entry.Kind == tree.File || source.Entry.Kind == tree.Symlink:
			files = append(files, rel)
		case source.Entry.Mode&fs.ModeNamedPipe != 0:
			err = syscall.Mkfifo(p.path(rel), uint32(source.Entry.Mode.Perm()))
		default:
			skipped = append(skipped, rel)
		}
		if err != nil {
			return skipped, err
		}
	}
	// Every directory exists now, so the files can be written in any order.
	return skipped, parallel.For(len(files), func(i int) error {
		return p.inParent(files[i], func(dir *os.File, name string) error {
			return projectStore.ExtractAt(files[i], p.target[files[i]], dir, name)
		})
	})
}

// inParent opens the directory that holds rel through the project root and
// calls write with it and rel's name.
func (p *restorePlan) inParent(rel string, write func(dir *os.File, name string) error) error {
	dir, err := p.projectRoot.Open(filepath.FromSlash(path.Dir(rel)))
	if err != nil {
		return err
	}
	defer dir.Close()
	return write(dir, path.Base(rel))
}

// clonableDirs returns the directories in paths that are missing from the
// project and can be cloned from the base in one call.
func (p *restorePlan) clonableDirs(projectStore *store.Store, paths []string) (map[string]bool, error) {
	var missing []string
	for _, rel := range paths {
		if _, isCurrent := p.current[rel]; !isCurrent && rel != "." && p.target[rel].Entry.Kind == tree.Dir {
			missing = append(missing, rel)
		}
	}
	return projectStore.UnchangedDirs(p.target, missing)
}

// restoreDirModeAndTimes sets the mode and times of every directory in paths,
// and of every directory that holds one of paths, deepest first. Writing or
// removing a path changes its directory's modification time, and the restore
// may have made a read-only directory writable, so this has to come last.
// It returns the directories it considered.
func (p *restorePlan) restoreDirModeAndTimes(paths []string) ([]string, error) {
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
	slices.SortFunc(sorted, func(a, b string) int { return tree.Depth(b) - tree.Depth(a) })
	parents := tree.NewParentChecker(p.root)
	for _, rel := range sorted {
		source, ok := p.target[rel]
		if !ok || source.Entry.Kind != tree.Dir {
			continue
		}
		if err := parents.Check(rel); err != nil {
			return nil, err
		}
		if err := p.setModeAndTimes(rel, source.Entry); err != nil {
			return nil, err
		}
	}
	return sorted, nil
}

func (p *restorePlan) path(rel string) string {
	return filepath.Join(p.root, filepath.FromSlash(rel))
}

// makeParentWritable lets the restore add or remove rel in its directory.
// The directory's recorded mode is set again at the end.
func (p *restorePlan) makeParentWritable(rel string) error {
	parent := filepath.FromSlash(path.Dir(rel))
	info, err := p.projectRoot.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o300 == 0o300 {
		return nil
	}
	return p.projectRoot.Chmod(parent, info.Mode().Perm()|0o300)
}

func (p *restorePlan) setModeAndTimes(rel string, entry tree.Entry) error {
	name := filepath.FromSlash(rel)
	if err := p.projectRoot.Chmod(name, clone.PermissionBits(entry.Mode)); err != nil {
		return err
	}
	modTime := time.Unix(0, entry.ModTime)
	return p.projectRoot.Chtimes(name, modTime, modTime)
}
