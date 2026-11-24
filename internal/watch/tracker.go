// Package watch runs a background process for each project that follows its
// files as they change, so a checkpoint only has to look at the paths that
// changed instead of scanning the whole project.
package watch

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/fsnotify"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

// markerTimeout is how long a checkpoint waits for its marker event. After
// that, the tracker stops trusting its events and compares the whole project
// instead.
const markerTimeout = 3 * time.Second

// tracker keeps a project's listing in memory and updates it from filesystem
// events, so a checkpoint only has to check the paths that changed.
type tracker struct {
	store     *store.Store
	root      string
	markerDir string
	matcher   atomic.Pointer[exclude.Matcher]
	stream    *fsnotify.Stream
	sequence  atomic.Uint64
	// markerPrefix tells this tracker's markers from those of an earlier
	// tracker in the same process, whose late events could otherwise count.
	markerPrefix string
	closeOnce    sync.Once

	mu sync.Mutex
	// dirty maps each path reported since the last checkpoint to whether
	// it was renamed.
	dirty          map[string]bool
	shouldRescan   bool
	hasRulesChange bool
	markers        map[string]chan struct{}

	// The fields below belong to the server goroutine, which handles one
	// request at a time.
	listing    tree.Listing
	unreadable []string
	// excluded are the excluded paths in the project. Removing one changes
	// nothing the listing holds, so every checkpoint checks they are still
	// there.
	excluded []string
	// unsavedFiles are files the last checkpoint could not store, usually
	// because they cannot be read. Every checkpoint tries them again, as a
	// scan would, so each one records them as unsaved.
	unsavedFiles []string
	// hardLinked are the listed files with more than one name. A write
	// through a name outside the project has no event here, so every
	// checkpoint checks them.
	hardLinked map[string]bool
	// children indexes the listing by folder. It is built the first time a
	// spelling check needs it, and nil until then.
	children          childIndex
	lastID            int
	isCaseInsensitive bool
}

// errStreamStopped means the tracker no longer watches the project, so the
// server has to stop and let clients scan instead.
var errStreamStopped = errors.New("watching stopped")

// streamStoppedError is a failure that stopped the tracker's watching. It
// matches errStreamStopped, and its message is its cause's alone, since
// whoever logs it already says the watcher stopped.
type streamStoppedError struct{ cause error }

func (e streamStoppedError) Error() string { return e.cause.Error() }

func (e streamStoppedError) Unwrap() error { return e.cause }

func (e streamStoppedError) Is(target error) bool { return target == errStreamStopped }

// newTracker starts watching the project in projectStore. Its first
// checkpoint compares the whole project, because changes made before it
// started are not known. Replaying the system's event history instead can
// miss the latest changes, which the history may not hold yet.
func newTracker(projectStore *store.Store) (*tracker, error) {
	markerDir, err := projectStore.MarkerDir()
	if err != nil {
		return nil, err
	}
	// FSEvents reports resolved paths, such as /private/var for /var.
	markerDir, err = filepath.EvalSymlinks(markerDir)
	if err != nil {
		return nil, err
	}
	t := &tracker{
		store:             projectStore,
		root:              projectStore.Root(),
		markerDir:         markerDir,
		markerPrefix:      rand.Text()[:8],
		dirty:             map[string]bool{},
		shouldRescan:      true,
		markers:           map[string]chan struct{}{},
		isCaseInsensitive: tree.IsCaseInsensitive(projectStore.Root()),
	}
	if err := t.startStream(); err != nil {
		return nil, err
	}
	return t, nil
}

// Close stops watching and saves the listing, so the next tracker or scan
// does not have to rebuild it from the layers.
// Closing it again does nothing.
func (t *tracker) Close() {
	t.closeOnce.Do(func() {
		t.stream.Close()
		t.saveListing()
	})
}

// Checkpoint records the project's current state. Before that, it waits until
// every change made before the call has been delivered.
func (t *tracker) Checkpoint(options store.CheckpointOptions) (store.Checkpoint, error) {
	isDelivered := t.waitForMarker()
	t.mu.Lock()
	dirty, shouldRescan, hasRulesChange := t.dirty, t.shouldRescan || !isDelivered, t.hasRulesChange
	t.dirty, t.shouldRescan, t.hasRulesChange = map[string]bool{}, false, false
	t.mu.Unlock()

	if hasRulesChange {
		// Paths the old rules excluded were never tracked, and a restore would
		// treat them as created after the checkpoint.
		t.stream.Close()
		if err := t.startStream(); err != nil {
			return store.Checkpoint{}, streamStoppedError{cause: err}
		}
		shouldRescan = true
	}
	checkpoint, err := t.checkpoint(options, dirty, shouldRescan)
	if err != nil {
		t.mu.Lock()
		t.shouldRescan = true
		t.mu.Unlock()
		t.replaceListing(nil)
		return checkpoint, err
	}
	return checkpoint, nil
}

// saveListingIfBehind saves the listing when the saved one is far behind. The
// server calls it when it goes quiet, so a watcher that is killed still
// leaves a recent listing.
func (t *tracker) saveListingIfBehind() {
	if t.store.IsListingBehind(t.lastID) {
		t.saveListing()
	}
}

// startStream reads the exclude rules and starts watching with them. On Linux
// the stream does not watch excluded directories, so it has to start over
// when the rules change.
func (t *tracker) startStream() error {
	matcher, err := exclude.New(t.root)
	if err != nil {
		return err
	}
	previous := t.matcher.Swap(matcher)
	stream, err := fsnotify.NewStream([]string{t.root, t.markerDir}, t.shouldSkip, t.handleEvents)
	if err != nil {
		t.matcher.Store(previous)
		return err
	}
	t.stream = stream
	return nil
}

// loadListing starts tracking from the stored listing at checkpoint id. The
// directories that checkpoint could not read stay unreadable until an event
// shows otherwise, so later checkpoints keep reporting them.
func (t *tracker) loadListing(id int) error {
	listing, err := t.store.ListingAt(id)
	if err != nil {
		return err
	}
	manifest, err := t.store.Manifest(id)
	if err != nil {
		return err
	}
	t.replaceListing(listing)
	t.lastID = id
	t.unreadable, t.unsavedFiles = nil, nil
	t.excluded = slices.Clone(manifest.Excluded)
	for _, rel := range manifest.Unreadable {
		if listing[rel].Kind == tree.Dir {
			t.unreadable = append(t.unreadable, rel)
		} else {
			t.unsavedFiles = append(t.unsavedFiles, rel)
		}
	}
	return nil
}

func (t *tracker) saveListing() {
	if t.listing != nil && t.lastID > 0 {
		if err := t.store.SaveListing(t.lastID, t.listing); err != nil {
			t.store.LogError(fmt.Errorf("saving the listing: %w", err))
		}
	}
}

// shouldSkip reports whether dir is excluded, so the stream does not need to
// watch it.
func (t *tracker) shouldSkip(dir string) bool {
	return t.isExcluded(t.matcher.Load(), dir)
}

func (t *tracker) handleEvents(events []fsnotify.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	matcher := t.matcher.Load()
	isExcludedDir := map[string]bool{}
	for _, event := range events {
		if done, ok := t.markers[event.Path]; ok {
			done <- struct{}{}
			delete(t.markers, event.Path)
			continue
		}
		if event.ShouldRescan {
			t.shouldRescan = true
		}
		if event.Path == filepath.Join(t.root, exclude.IgnoreFile) {
			t.hasRulesChange = true
		}
		// Installs and builds write many files into excluded directories,
		// and none of them needs a stat.
		parent := filepath.Dir(event.Path)
		isExcluded, isKnown := isExcludedDir[parent]
		if !isKnown {
			isExcluded = t.isExcluded(matcher, parent)
			isExcludedDir[parent] = isExcluded
		}
		if isExcluded {
			// A new name in an excluded directory for a file the project
			// lists changes that file's link count, and later writes through
			// it change its content, with no event for the listed name.
			if event.IsHardLink {
				t.dirty[event.Path] = t.dirty[event.Path] || event.IsRenamed
			}
			continue
		}
		if t.isInRoot(event.Path) {
			t.dirty[event.Path] = t.dirty[event.Path] || event.IsRenamed
		}
		// Creating, removing or renaming a path also changes its directory's
		// times, but the event names only the path itself.
		if t.isInRoot(parent) {
			markDirty(t.dirty, parent)
		}
	}
}

// isExcluded reports whether matcher excludes the directory dir or one of its
// parents.
func (t *tracker) isExcluded(matcher *exclude.Matcher, dir string) bool {
	rel, err := filepath.Rel(t.root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return matcher.Excludes(filepath.ToSlash(rel), true)
}

func (t *tracker) isInRoot(fullPath string) bool {
	return fullPath == t.root || strings.HasPrefix(fullPath, t.root+string(filepath.Separator))
}

func (t *tracker) checkpoint(options store.CheckpointOptions, dirty map[string]bool, shouldRescan bool) (store.Checkpoint, error) {
	latest, hasLatest, err := t.store.Latest()
	if err != nil {
		return store.Checkpoint{}, err
	}
	if !hasLatest {
		checkpoint, err := t.store.Take(options)
		if err != nil {
			return checkpoint, err
		}
		// Take saved the listing it scanned, since there was none, and every
		// change since then is in dirty, so the next checkpoint does not need
		// a full scan.
		return checkpoint, t.loadListing(checkpoint.ID)
	}
	if t.listing == nil || latest.ID != t.lastID {
		if err := t.loadListing(latest.ID); err != nil {
			return store.Checkpoint{}, err
		}
		shouldRescan = true
	}

	var changed, deleted []string
	if !shouldRescan {
		changed, deleted, shouldRescan, err = t.applyEvents(dirty)
		if err != nil {
			return store.Checkpoint{}, err
		}
	}
	if shouldRescan {
		current, skipped, err := tree.Scan(t.root, t.matcher.Load())
		if err != nil {
			return store.Checkpoint{}, err
		}
		changed, deleted = tree.Changed(t.listing, current)
		t.replaceListing(current)
		t.unreadable, t.excluded = skipped.Unreadable, skipped.Excluded
	}

	checkpoint, err := t.store.Commit(latest.ID, options, store.Changes{
		Listing:         t.listing,
		Changed:         changed,
		Deleted:         deleted,
		Unreadable:      t.unreadable,
		Excluded:        t.excluded,
		ExcludePatterns: t.matcher.Load().Patterns(),
	})
	if errors.Is(err, store.ErrStale) {
		t.replaceListing(nil)
		return t.checkpoint(options, nil, true)
	}
	if err != nil {
		return store.Checkpoint{}, err
	}
	t.lastID = checkpoint.ID
	// Commit drops a changed file it could not store from the listing.
	t.unsavedFiles = slices.DeleteFunc(changed, func(rel string) bool {
		_, isListed := t.listing[rel]
		return isListed
	})
	return checkpoint, nil
}

// applyEvents updates the listing with the paths in dirty, and the files
// whose last change could not be saved or that have names outside the
// project. It reports whether a full scan is needed instead.
func (t *tracker) applyEvents(dirty map[string]bool) (changed, deleted []string, shouldRescan bool, err error) {
	for _, rel := range t.unsavedFiles {
		markDirty(dirty, t.path(rel))
	}
	t.markChangedHardLinks(dirty)
	changed, deleted, err = t.applyDirty(dirty)
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, errNeedsFullComparison) || (err == nil && canEventsArriveLate && t.hasUnreportedChange()) {
		// applyDirty changed the listing in place, and a full comparison
		// must start from the listing of the last checkpoint, or what it
		// already applied would not count as changed.
		if err := t.loadListing(t.lastID); err != nil {
			return nil, nil, false, err
		}
		return nil, nil, true, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	t.unreadable = slices.DeleteFunc(t.unreadable, t.isReadableOrGone)
	touched := t.touchedPaths(dirty, changed, deleted)
	t.excluded = slices.DeleteFunc(t.excluded, func(rel string) bool {
		return isTouched(touched, rel) && t.isStaleExcluded(rel)
	})
	t.updateHardLinked(changed, deleted)
	t.children.update(changed, deleted)
	return changed, deleted, false, nil
}

// touchedPaths returns the paths a batch of events reported, changed or
// deleted. Removing or renaming an excluded path, or a folder above it,
// reports that path or folder, and changes the entry of the folder that
// held it, so only excluded paths at or below one of these, or directly in
// one, can have gone. The root counts only when its own entry changed.
func (t *tracker) touchedPaths(dirty map[string]bool, changed, deleted []string) map[string]bool {
	touched := make(map[string]bool, len(dirty)+len(changed)+len(deleted))
	for fullPath := range dirty {
		if rel, err := filepath.Rel(t.root, fullPath); err == nil && rel != "." {
			touched[filepath.ToSlash(rel)] = true
		}
	}
	for _, rel := range slices.Concat(changed, deleted) {
		touched[rel] = true
	}
	return touched
}

// isTouched reports whether rel or a folder above it is in touched, or rel
// is directly in the root and the root is. Events name a path the way the
// command spelled it, so on a filesystem that ignores case only the change
// to the folder holding rel may match how rel is recorded.
func isTouched(touched map[string]bool, rel string) bool {
	for current := rel; current != "."; current = path.Dir(current) {
		if touched[current] {
			return true
		}
	}
	return path.Dir(rel) == "." && touched["."]
}

// replaceListing makes listing the tracker's view of the project, and drops
// what was derived from the old one.
func (t *tracker) replaceListing(listing tree.Listing) {
	t.listing = listing
	t.hardLinked = hardLinkedFiles(listing)
	t.children = nil
}

// markChangedHardLinks marks the listed files with several names whose stat
// changed. A write through a name outside the project has no event here, but
// a stat of the project's name shows it.
func (t *tracker) markChangedHardLinks(dirty map[string]bool) {
	for rel := range t.hardLinked {
		if entry, err := tree.Stat(t.path(rel)); err != nil || entry != t.listing[rel] {
			markDirty(dirty, t.path(rel))
		}
	}
}

// hardLinkedFiles returns the files in listing with more than one name.
func hardLinkedFiles(listing tree.Listing) map[string]bool {
	hardLinked := map[string]bool{}
	for rel, entry := range listing {
		if entry.LinkCount > 1 {
			hardLinked[rel] = true
		}
	}
	return hardLinked
}

// updateHardLinked keeps t.hardLinked in step with the listing after the
// paths in changed and deleted were applied.
func (t *tracker) updateHardLinked(changed, deleted []string) {
	for _, rel := range deleted {
		delete(t.hardLinked, rel)
	}
	for _, rel := range changed {
		if t.listing[rel].LinkCount > 1 {
			t.hardLinked[rel] = true
		} else {
			delete(t.hardLinked, rel)
		}
	}
}

func (t *tracker) path(rel string) string {
	return filepath.Join(t.root, filepath.FromSlash(rel))
}

// markDirty adds fullPath to dirty, keeping whether an event already
// reported it renamed.
func markDirty(dirty map[string]bool, fullPath string) {
	if _, isDirty := dirty[fullPath]; !isDirty {
		dirty[fullPath] = false
	}
}

// isStaleExcluded reports whether the excluded path rel is gone or tracked
// now. On a filesystem that ignores case, rel and its folder must also still
// be spelled the way they are recorded.
func (t *tracker) isStaleExcluded(rel string) bool {
	// A path the listing holds is tracked now, such as a file in place of an
	// excluded folder.
	if _, isListed := t.listing[rel]; isListed {
		return true
	}
	if parent := path.Dir(rel); parent != "." {
		if entry, isListed := t.listing[parent]; !isListed || entry.Kind != tree.Dir {
			return true
		}
	}
	if !t.isCaseInsensitive {
		_, err := os.Lstat(t.path(rel))
		return errors.Is(err, fs.ErrNotExist)
	}
	// A stat finds rel under any spelling, such as after node_modules was
	// renamed to Node_modules, which the rules no longer exclude.
	storedName, err := tree.StoredName(t.path(rel))
	return err != nil || storedName != path.Base(rel)
}

// isReadableOrGone reports whether the directory rel can be listed now, or
// no longer exists.
func (t *tracker) isReadableOrGone(rel string) bool {
	err := unix.Access(t.path(rel), unix.R_OK|unix.X_OK)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// waitForMarker writes a file next to the store and waits for its event.
// Events arrive in order, so once this one arrives, every earlier change has
// arrived too.
func (t *tracker) waitForMarker() bool {
	markerName := fmt.Sprintf("%s-%d-%d", t.markerPrefix, os.Getpid(), t.sequence.Add(1))
	markerPath := filepath.Join(t.markerDir, markerName)
	done := make(chan struct{}, 1)
	t.mu.Lock()
	t.markers[markerPath] = done
	t.mu.Unlock()
	// A marker left behind is harmless, as every checkpoint uses a new name.
	defer func() { _ = t.store.RemoveMarker(markerName) }()

	if err := t.store.WriteMarker(markerName); err != nil {
		t.mu.Lock()
		delete(t.markers, markerPath)
		t.mu.Unlock()
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(markerTimeout):
		t.mu.Lock()
		delete(t.markers, markerPath)
		t.mu.Unlock()
		return false
	}
}

// applyDirty updates the listing for every path reported since the last
// checkpoint and returns what changed. A path it has no permission to read
// returns an error matching fs.ErrPermission, and the caller compares the
// whole project instead, as a scan handles unreadable paths.
func (t *tracker) applyDirty(dirty map[string]bool) (changed, deleted []string, err error) {
	changes := newChangeSet(t.listing)
	matcher := t.matcher.Load()
	parents := newParentDirs(t.root)
	defer parents.close()
	storedDirs := map[string]string{}
	dirty = t.withHardLinks(dirty)
	for _, fullPath := range slices.Sorted(maps.Keys(dirty)) {
		isRenamed := dirty[fullPath]
		if t.isCaseInsensitive {
			fullPath = t.storedSpelling(parents, storedDirs, fullPath)
		}
		if err := t.applyPath(changes, matcher, parents, fullPath, isRenamed); err != nil {
			return nil, nil, err
		}
	}
	if changes.hasLinkCountChange {
		return nil, nil, errNeedsFullComparison
	}
	if t.isCaseInsensitive {
		t.removeStaleSpellings(parents, changes)
	}
	changes.finish()
	if len(changes.excluded) > 0 {
		t.excluded = slices.Compact(slices.Sorted(slices.Values(append(t.excluded, changes.excluded...))))
	}
	return slices.Sorted(maps.Keys(changes.changed)), slices.Sorted(maps.Keys(changes.deleted)), nil
}

// withHardLinks adds the other names of every dirty file, named pipe or
// symlink that has several.
// A write through one name changes the content of all of them, but the
// event names only the one that was written.
func (t *tracker) withHardLinks(dirty map[string]bool) map[string]bool {
	var inodes map[uint64]bool
	for fullPath := range dirty {
		info, err := os.Lstat(fullPath)
		if err != nil || info.IsDir() {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			if inodes == nil {
				inodes = map[uint64]bool{}
			}
			inodes[stat.Ino] = true
		}
	}
	if len(inodes) == 0 {
		return dirty
	}
	// The listed names of an inode with several names are already checked.
	for rel := range t.hardLinked {
		delete(inodes, t.listing[rel].Inode)
	}
	if len(inodes) == 0 {
		return dirty
	}
	for rel, entry := range t.listing {
		if entry.Kind != tree.Dir && inodes[entry.Inode] {
			markDirty(dirty, t.path(rel))
		}
	}
	return dirty
}

// applyPath updates the listing for one reported path. A path below a
// symlink or a file is gone from the project, even if a stat through the
// link would find something.
func (t *tracker) applyPath(changes *changeSet, matcher *exclude.Matcher, parents *parentDirs, fullPath string, isRenamed bool) error {
	rel, err := filepath.Rel(t.root, fullPath)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	previous, wasTracked := changes.listing[rel]
	wasDir := wasTracked && previous.Kind == tree.Dir
	entry, storedName, err := lookUp(parents, rel)
	switch {
	case errors.Is(err, errPathGone):
		changes.removeGone(rel, wasTracked, wasDir)
		return nil
	case err != nil:
		return err
	case storedName != path.Base(rel):
		// The event names the path before a rename that changed only case or
		// Unicode form, and the stat reached the renamed file.
		changes.removeTree(rel, wasDir)
		changes.uncertainDirs[path.Dir(rel)] = true
		return t.applyPath(changes, matcher, parents, filepath.Join(filepath.Dir(fullPath), storedName), isRenamed)
	case matcher.Excludes(rel, entry.Kind == tree.Dir):
		// What the listing held here, such as a file where an excluded
		// directory now is, is gone from what the project tracks.
		changes.removeExcluded(rel, wasDir, isTopmostExcluded(matcher, rel))
		return nil
	}
	// A directory renamed into place, even the one that was here before, can
	// hold files that changed elsewhere and have no events of their own.
	isReplaced := wasDir && (entry.Kind != tree.Dir || previous.Inode != entry.Inode || isRenamed)
	if isReplaced {
		changes.replaceUnder(rel)
	}
	if wasTracked && hasLinkCountChange(previous, entry) {
		changes.hasLinkCountChange = true
	}
	if !wasTracked {
		changes.uncertainDirs[path.Dir(rel)] = true
	}
	changes.update(rel, entry)
	if mayHoldUnseenFiles(entry, previous, wasDir, isReplaced) {
		return t.addUnder(changes, matcher, fullPath)
	}
	return nil
}

// isTopmostExcluded reports whether the excluded path rel is in a folder the
// project tracks, rather than inside another excluded path.
func isTopmostExcluded(matcher *exclude.Matcher, rel string) bool {
	parent := path.Dir(rel)
	return parent == "." || !matcher.Excludes(parent, true)
}

// hasLinkCountChange reports whether the same file gained or lost a name.
func hasLinkCountChange(previous, entry tree.Entry) bool {
	return entry.Kind != tree.Dir && entry.Inode == previous.Inode && entry.LinkCount != previous.LinkCount
}

// mayHoldUnseenFiles reports whether a directory can hold files that never
// had events of their own: it is new or replaced, or its mode changed and it
// may have become readable.
func mayHoldUnseenFiles(entry, previous tree.Entry, wasDir, isReplaced bool) bool {
	return entry.Kind == tree.Dir && (!wasDir || isReplaced || previous.Mode != entry.Mode)
}

// errNeedsFullComparison reports that events cannot tell what changed, as
// when a listed file gained a name the events did not report.
var errNeedsFullComparison = errors.New("a full comparison is needed")

// errPathGone reports that a path is no longer in the project.
var errPathGone = errors.New("path is gone")

// lookUp returns the entry at rel and the name its directory stores for it,
// through the directory reached one name at a time. A path below something
// that is not a directory, or that no longer exists, is gone.
func lookUp(parents *parentDirs, rel string) (tree.Entry, string, error) {
	if rel == "." {
		dir, err := parents.open(".")
		if err != nil {
			return tree.Entry{}, "", err
		}
		entry, err := tree.StatAt(dir, ".")
		return entry, ".", err
	}
	dir, err := parents.open(path.Dir(rel))
	if isGone(err) {
		return tree.Entry{}, "", errPathGone
	}
	if err != nil {
		return tree.Entry{}, "", err
	}
	entry, err := tree.StatAt(dir, path.Base(rel))
	if isGone(err) {
		return tree.Entry{}, "", errPathGone
	}
	if err != nil {
		return tree.Entry{}, "", err
	}
	storedName, err := tree.StoredNameAt(dir, path.Base(rel))
	if isGone(err) {
		return tree.Entry{}, "", errPathGone
	}
	return entry, storedName, err
}

// isGone reports whether err means the path does not exist, which includes a
// parent that a file or a symlink has replaced since the parents were
// checked.
func isGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP)
}

// addUnder adds everything inside a new directory to the listing, in case some
// of its files were created before their events arrived.
func (t *tracker) addUnder(changes *changeSet, matcher *exclude.Matcher, dir string) error {
	rel, err := filepath.Rel(t.root, dir)
	if err != nil {
		return err
	}
	// A folder that cannot be read returns fs.ErrPermission, and
	// applyEvents compares the whole project instead.
	found, skipped, err := tree.ScanDir(t.root, filepath.ToSlash(rel), matcher)
	if err != nil {
		return err
	}
	changes.excluded = append(changes.excluded, skipped.Excluded...)
	for rel, entry := range found {
		changes.update(rel, entry)
	}
	return nil
}
