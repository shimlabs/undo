package restore

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

// apply makes the changes. Every write, removal and mode change goes through
// a root opened at the project, so a directory swapped for a symlink while
// the restore runs cannot lead it outside the project.
func (p *restorePlan) apply(projectStore *store.Store, id int) error {
	// The project folder itself may have been deleted. Its mode and times
	// are set at the end, with the other directories.
	if err := os.MkdirAll(p.root, 0o700); err != nil {
		return err
	}
	projectRoot, err := tree.OpenRoot(p.root)
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
	dirs, err := p.restoreDirModeAndTimes(slices.Concat(p.changes.Written, p.changes.Removed, p.unchangedDirs))
	if err != nil {
		return err
	}
	written := slices.Concat(p.changes.RestoredPaths(), dirs)
	entries := p.restoredEntries(written)
	for rel, entry := range p.unchanged {
		if _, isWritten := entries[rel]; !isWritten {
			entries[rel] = entry
		}
	}
	return projectStore.SaveRestore(id, entries)
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
	tree.SortDeepestFirst(paths)
	parents := tree.NewParentChecker(p.root)
	parent := parentDir{root: p.projectRoot}
	defer parent.close()
	for _, rel := range paths {
		if err := parents.Check(rel); err != nil {
			return err
		}
		if _, err := parent.open(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
	tree.SortParentsFirst(paths)
	clonable, err := p.clonableDirs(projectStore, paths)
	if err != nil {
		return nil, err
	}
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	p.cloned = map[string]bool{}
	pending := pendingWrites{parentDir: parentDir{root: p.projectRoot}}
	defer pending.parentDir.close()
	parents := tree.NewParentChecker(p.root)
	for _, rel := range paths {
		if err := p.writePath(rel, clonable[rel], extractor, parents, &pending); err != nil {
			return pending.skipped, err
		}
	}
	pending.parentDir.close()
	p.keepOccupied(pending.occupied)
	// Every directory exists now, so the files can be written in any order.
	return pending.skipped, p.writeFiles(extractor, pending.files)
}

// pendingWrites is what writing paths parents first leaves for later: the
// files to write in parallel, the paths that what the removals left in
// place occupies, and the paths that cannot be recreated. parentDir is the
// directory of the path being written.
type pendingWrites struct {
	files     []string
	occupied  []string
	skipped   []string
	parentDir parentDir
}

// writePath writes the directory or special file rel, or adds it to
// pending. A directory that is missing and isClonable is cloned whole.
func (p *restorePlan) writePath(rel string, isClonable bool, extractor *store.Extractor, parents *tree.ParentChecker, pending *pendingWrites) error {
	if rel != "." && p.cloned[path.Dir(rel)] {
		p.cloned[rel] = true
		return nil
	}
	if isAtOrInside(rel, pending.occupied) {
		return nil
	}
	source := p.target[rel]
	if err := parents.Check(rel); err != nil {
		return err
	}
	isOccupied, err := pending.isOccupied(rel, source.Entry)
	if err != nil {
		return err
	}
	if isOccupied {
		pending.occupied = append(pending.occupied, rel)
		return nil
	}
	switch {
	case isClonable && p.isMissing(rel):
		p.cloned[rel] = true
		return p.inParent(rel, func(dir *os.File, name string) error {
			return extractor.ExtractBaseDir(rel, dir, name)
		})
	case source.Entry.Kind == tree.File || source.Entry.Kind == tree.Symlink:
		pending.files = append(pending.files, rel)
		return nil
	default:
		isWritten, err := p.writeOther(extractor, rel, source)
		if !isWritten {
			pending.skipped = append(pending.skipped, rel)
		}
		return err
	}
}

// isOccupied reports whether something the removals left in place stands
// where the restore must write entry at rel, so writing it would fail. On a
// filesystem that ignores case, a directory renamed only in case that holds
// excluded content survives its removal under the target's name; a locked
// file survives too.
func (w *pendingWrites) isOccupied(rel string, entry tree.Entry) (bool, error) {
	if rel == "." {
		return false, nil
	}
	dir, err := w.parentDir.open(rel)
	// A parent that is missing could not be written, so nothing stands in
	// the way of rel.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	found, err := tree.StatAt(dir, path.Base(rel))
	if err != nil {
		return false, nil //nolint:nilerr // nothing in the way is the usual case
	}
	return (found.Kind == tree.Dir) != (entry.Kind == tree.Dir), nil
}

// keepOccupied takes the occupied paths, and everything under them, out of
// the written paths and reports them as kept.
func (p *restorePlan) keepOccupied(occupied []string) {
	if len(occupied) == 0 {
		return
	}
	p.changes.Written = slices.DeleteFunc(p.changes.Written, func(rel string) bool { return isAtOrInside(rel, occupied) })
	p.changes.Kept = append(p.changes.Kept, occupied...)
	slices.Sort(p.changes.Kept)
	for _, rel := range occupied {
		p.changes.KeepReasons[rel] = KeptOccupied
		if info, err := p.projectRoot.Lstat(filepath.FromSlash(rel)); err == nil && info.IsDir() {
			p.changes.Dirs[rel] = true
		}
	}
}

// writeFiles writes files from the store in parallel. A file whose stored
// copy is gone is added to the lost paths instead.
func (p *restorePlan) writeFiles(extractor *store.Extractor, files []string) error {
	isLost := make([]bool, len(files))
	err := parallel.For(len(files), func(i int) error {
		err := p.inParent(files[i], func(dir *os.File, name string) error {
			return extractor.ExtractAt(files[i], p.target[files[i]], dir, name)
		})
		if errors.Is(err, store.ErrStoredFileMissing) {
			isLost[i] = true
			return nil
		}
		return err
	})
	for i, rel := range files {
		if isLost[i] {
			p.changes.Lost = append(p.changes.Lost, rel)
		}
	}
	return err
}

// writeOther creates the directory or named pipe source describes, and
// reports whether it could. Sockets and devices cannot be restored.
func (p *restorePlan) writeOther(extractor *store.Extractor, rel string, source store.Source) (bool, error) {
	entry := source.Entry
	switch {
	case entry.Kind == tree.Dir:
		if err := p.projectRoot.MkdirAll(filepath.FromSlash(rel), 0o700); err != nil {
			return true, err
		}
		if err := p.restoreNameCase(rel); err != nil {
			return true, err
		}
		dir, err := openDirIn(p.projectRoot, rel)
		if err != nil {
			return true, err
		}
		defer dir.Close()
		return true, extractor.CopyDirAttributes(rel, source, dir)
	case entry.Mode&fs.ModeNamedPipe != 0:
		return true, p.inParent(rel, func(dir *os.File, name string) error {
			return clone.MkfifoAt(dir, name, entry.Mode)
		})
	default:
		return false, nil
	}
}

// isMissing reports whether nothing is at rel now. A directory renamed only
// in case, which a filesystem that ignores case finds under the target name,
// survives its removal when it holds excluded content.
func (p *restorePlan) isMissing(rel string) bool {
	_, err := p.projectRoot.Lstat(filepath.FromSlash(rel))
	return errors.Is(err, fs.ErrNotExist)
}

// restoreNameCase gives the directory rel the exact name the checkpoint
// recorded. On a filesystem that ignores case, a directory renamed only in
// case is found under the target name; one that holds excluded content was
// not removed, so it keeps the new spelling until it is renamed back.
func (p *restorePlan) restoreNameCase(rel string) error {
	if rel == "." {
		return nil
	}
	storedName, err := tree.StoredName(p.path(rel))
	if err != nil || storedName == path.Base(rel) {
		return err
	}
	current := filepath.FromSlash(path.Join(path.Dir(rel), storedName))
	err = p.projectRoot.Rename(current, filepath.FromSlash(rel))
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// APFS renames a directory only if it is writable. Its mode is set back
	// with the other directories the restore wrote.
	info, statErr := p.projectRoot.Lstat(current)
	if statErr != nil || !info.IsDir() {
		return err
	}
	if err := p.projectRoot.Chmod(current, info.Mode().Perm()|0o200); err != nil {
		return err
	}
	return p.projectRoot.Rename(current, filepath.FromSlash(rel))
}

// inParent opens the directory that holds rel through the project root and
// calls write with it and rel's name.
func (p *restorePlan) inParent(rel string, write func(dir *os.File, name string) error) error {
	dir, err := openDirIn(p.projectRoot, path.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	return clone.JoinErrorPath(path.Dir(rel), write(dir, path.Base(rel)))
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
	tree.SortDeepestFirst(sorted)
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

func (p *restorePlan) setModeAndTimes(rel string, entry tree.Entry) error {
	name := filepath.FromSlash(rel)
	err := p.projectRoot.Chmod(name, clone.PermissionBits(entry.Mode))
	if errors.Is(err, fs.ErrPermission) && p.cloned[rel] {
		// A directory cloned whole keeps a lock flag it had at the checkpoint,
		// which forbids setting its mode and times. A restore brings paths
		// back unlocked, as it does every file it writes on its own.
		if err := p.inParent(rel, clone.UnlockAt); err != nil {
			return err
		}
		err = p.projectRoot.Chmod(name, clone.PermissionBits(entry.Mode))
	}
	if err != nil {
		return err
	}
	modTime := time.Unix(0, entry.ModTime)
	return p.projectRoot.Chtimes(name, modTime, modTime)
}
