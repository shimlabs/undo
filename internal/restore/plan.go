package restore

import (
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

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
	// current is the project as it is, less the paths the plan drops.
	current tree.Listing
	// scanned is the project as it is, with every path.
	scanned tree.Listing
	changes Changes
	// unchanged holds the paths an earlier restore to the same checkpoint
	// wrote and nothing changed since, so they still match the target.
	unchanged tree.Listing
	// keepReasons says why each path the restore leaves as it is was kept.
	keepReasons map[string]KeepReason
	// cloned holds the paths written as part of a whole directory cloned
	// from the base, which keeps the lock flags they had at the checkpoint.
	cloned map[string]bool
}

// planRestore plans a restore of checkpoint id. It never touches a path the
// checkpoint did not save: paths its exclude rules left out, paths in
// directories that could not be read then or now, and files that cannot be
// read now, which the safety checkpoint cannot save either. A path that only
// today's rules exclude is written back if it is missing, but never
// overwritten, and nothing inside it is touched.
func planRestore(projectStore *store.Store, id int) (restorePlan, error) {
	inputs, err := loadRestoreInputs(projectStore, id)
	if err != nil {
		return restorePlan{}, err
	}
	plan := restorePlan{root: projectStore.Root(), target: inputs.target, current: maps.Clone(inputs.current), scanned: inputs.current}
	plan.planChanges(inputs)
	return plan, nil
}

// restoreInputs is what planning a restore reads from the store and the
// project.
type restoreInputs struct {
	manifest    store.Manifest
	matcherThen *exclude.Matcher
	matcherNow  *exclude.Matcher
	target      map[string]store.Source
	current     tree.Listing
	skipped     tree.Skipped
	// restored holds the entries an earlier restore to the same checkpoint
	// left.
	restored tree.Listing
}

func loadRestoreInputs(projectStore *store.Store, id int) (restoreInputs, error) {
	root := projectStore.Root()
	matcherNow, err := exclude.New(root)
	if err != nil {
		return restoreInputs{}, err
	}
	manifest, err := projectStore.Manifest(id)
	if err != nil {
		return restoreInputs{}, err
	}
	matcherThen := matcherNow
	if manifest.ExcludePatterns != nil {
		matcherThen = exclude.FromPatterns(manifest.ExcludePatterns)
	}
	target, err := projectStore.State(id)
	if err != nil {
		return restoreInputs{}, err
	}
	current, skipped, err := tree.Scan(root, matcherNow)
	if err != nil {
		return restoreInputs{}, err
	}
	restored, err := projectStore.RestoredEntries(id)
	if err != nil {
		return restoreInputs{}, err
	}
	return restoreInputs{
		manifest: manifest, matcherThen: matcherThen, matcherNow: matcherNow,
		target: target, current: current, skipped: skipped, restored: restored,
	}, nil
}

// planChanges works out the changes of the plan, leaving out every path the
// restore must not touch.
func (p *restorePlan) planChanges(inputs restoreInputs) {
	lost := p.lostPaths()
	p.drop(lost)
	p.keepReasons = map[string]KeepReason{}
	untracked := p.dropUnsaved(inputs.matcherThen, inputs.matcherNow, inputs.manifest.Unreadable, inputs.skipped.Unreadable)
	written, removed := tree.Changed(p.current, p.targetListing())
	// Paths that still match are left alone, so they need none of the checks
	// below.
	p.unchanged = unchangedEntries(inputs.restored, p.current, written)
	isUnchanged := func(rel string) bool {
		_, isUnchanged := p.unchanged[rel]
		return isUnchanged
	}
	written = slices.DeleteFunc(written, isUnchanged)
	// The safety checkpoint could not save a file undo cannot read, so the
	// restore must not change it either.
	unreadable := p.unreadableFiles(slices.Concat(written, removed))
	p.drop(unreadable)
	p.keep(KeptUnreadable, unreadable)
	// A locked file cannot be replaced or removed, so it is kept as it is.
	locked := p.lockedPaths(slices.Concat(written, removed))
	p.drop(locked)
	p.keep(KeptLocked, locked)
	// A directory that holds paths the restore must not touch, at any
	// depth, cannot be replaced by a file either.
	blocked := p.dirsHoldingUntracked(written, slices.Concat(untracked, unreadable, locked, inputs.skipped.Excluded, inputs.skipped.Unreadable))
	p.drop(blocked)
	p.keep(KeptOccupied, blocked)
	if len(unreadable)+len(locked)+len(blocked) > 0 {
		written, removed = tree.Changed(p.current, p.targetListing())
	}
	kept := slices.Sorted(maps.Keys(p.keepReasons))
	written = slices.DeleteFunc(written, isInsideAny(kept))
	written = slices.DeleteFunc(written, isUnchanged)
	removed = slices.DeleteFunc(removed, isInsideAny(kept))
	// A checkpoint of a project that was missing has no root, but removing
	// the project folder itself would act outside it, so it stays, empty.
	removed = slices.DeleteFunc(removed, func(rel string) bool { return rel == "." })
	p.changes = Changes{
		Written: written, Removed: removed, Kept: kept, KeepReasons: p.keepReasons, Dirs: p.dirsAmong(written, removed, kept),
		Unstable: inputs.manifest.Unstable, Lost: lost,
	}
}

// unchangedEntries returns the entries of the paths in written that an
// earlier restore to the same checkpoint wrote, and that nothing changed
// since.
func unchangedEntries(restored, current tree.Listing, written []string) tree.Listing {
	unchanged := tree.Listing{}
	for _, rel := range written {
		if entry, isRestored := restored[rel]; isRestored && current[rel] == entry {
			unchanged[rel] = entry
		}
	}
	return unchanged
}

// dropUnsaved removes from the plan every path the checkpoint did not save:
// paths its exclude rules left out and paths in directories unreadable then
// or now. It records the existing paths the restore keeps, because only
// today's rules exclude them or they cannot be read now, and returns the
// paths it dropped from the project's current state.
func (p *restorePlan) dropUnsaved(matcherThen, matcherNow *exclude.Matcher, unreadableThen, unreadableNow []string) (untracked []string) {
	unreadable := slices.Concat(unreadableThen, unreadableNow)
	isUnsaved := func(rel string, isDir bool) bool {
		return matcherThen.Excludes(rel, isDir) || isAtOrInside(rel, unreadable)
	}
	for rel, entry := range p.current {
		if isUnsaved(rel, entry.Kind == tree.Dir) {
			delete(p.current, rel)
			untracked = append(untracked, rel)
		}
	}
	for rel, source := range p.target {
		isDir := source.Entry.Kind == tree.Dir
		switch {
		case isUnsaved(rel, isDir):
			delete(p.target, rel)
			if slices.Contains(unreadableNow, rel) {
				p.keep(KeptUnreadable, []string{rel})
			}
		case matcherNow.Excludes(rel, isDir) && exists(p.path(rel)):
			delete(p.target, rel)
			p.keep(KeptExcluded, []string{rel})
		}
	}
	return untracked
}

// keep records paths as kept for reason.
func (p *restorePlan) keep(reason KeepReason, paths []string) {
	for _, rel := range paths {
		p.keepReasons[rel] = reason
	}
}

// dirsAmong returns the written, removed and kept paths that are
// directories: in the target for a written path, and in the project for a
// removed or kept one.
func (p *restorePlan) dirsAmong(written, removed, kept []string) map[string]bool {
	dirs := map[string]bool{}
	for _, rel := range written {
		if p.target[rel].Entry.Kind == tree.Dir {
			dirs[rel] = true
		}
	}
	for _, rel := range slices.Concat(removed, kept) {
		if p.scanned[rel].Kind == tree.Dir {
			dirs[rel] = true
		}
	}
	return dirs
}

// lostPaths returns the paths the target holds whose stored copy is gone,
// sorted.
func (p *restorePlan) lostPaths() []string {
	var lost []string
	for rel, source := range p.target {
		if source.IsLost {
			lost = append(lost, rel)
		}
	}
	slices.Sort(lost)
	return lost
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
// files or symlinks but hold, at any depth, one of the untracked paths, such
// as excluded or unreadable ones.
func (p *restorePlan) dirsHoldingUntracked(paths, untracked []string) []string {
	holdsUntracked := map[string]bool{}
	for _, rel := range untracked {
		for dir := path.Dir(rel); dir != "." && !holdsUntracked[dir]; dir = path.Dir(dir) {
			holdsUntracked[dir] = true
		}
	}
	var blocked []string
	for _, rel := range paths {
		if p.current[rel].Kind == tree.Dir && p.target[rel].Entry.Kind != tree.Dir && holdsUntracked[rel] {
			blocked = append(blocked, rel)
		}
	}
	return blocked
}

// lockedPaths returns the paths among paths that exist now and are locked,
// as Finder's Locked checkbox does, or that lie in a locked directory, to
// which nothing can be added and from which nothing can be removed.
func (p *restorePlan) lockedPaths(paths []string) []string {
	isDirLocked := map[string]bool{}
	var locked []string
	for _, rel := range paths {
		dir := path.Dir(rel)
		isParentLocked, isKnown := isDirLocked[dir]
		if !isKnown {
			isParentLocked = clone.IsLocked(p.path(dir))
			isDirLocked[dir] = isParentLocked
		}
		_, isCurrent := p.current[rel]
		if rel != "." && isParentLocked || isCurrent && clone.IsLocked(p.path(rel)) {
			locked = append(locked, rel)
		}
	}
	return locked
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
