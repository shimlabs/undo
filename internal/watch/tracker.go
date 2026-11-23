// Package watch runs a background process for each project that follows its
// files as they change, so a checkpoint only has to look at the paths that
// changed instead of scanning the whole project.
package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
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
	closeOnce sync.Once

	mu sync.Mutex
	// dirty maps each path reported since the last checkpoint to whether
	// it was renamed.
	dirty          map[string]bool
	shouldRescan   bool
	hasRulesChange bool
	markers        map[string]chan fsnotify.EventID
	// historyDone is closed once a resumed stream has delivered the changes
	// made before it started.
	historyDone chan struct{}

	// The fields below belong to the server goroutine, which handles one
	// request at a time.
	listing    tree.Listing
	unreadable []string
	// unsavedFiles are files the last checkpoint could not store, usually
	// because they cannot be read. Every checkpoint tries them again, as a
	// scan would, so each one records them as unsaved.
	unsavedFiles []string
	// hardLinked are the listed files with more than one name. A write
	// through a name outside the project has no event here, so every
	// checkpoint checks them.
	hardLinked map[string]bool
	lastID     int
	// position is where a new tracker can resume the events, as of the last
	// checkpoint whose marker arrived.
	position streamPosition
}

// errStreamStopped means the tracker no longer watches the project, so the
// server has to stop and let clients scan instead.
var errStreamStopped = errors.New("watching stopped")

// newTracker starts watching the project in projectStore. Where the system
// keeps an event history, it resumes after the previous tracker's last
// checkpoint. Otherwise its first checkpoint compares the whole project,
// because changes made before it started are not known.
func newTracker(projectStore *store.Store) (*tracker, error) {
	markerDir := projectStore.MarkerDir()
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return nil, err
	}
	markerDir, err := filepath.EvalSymlinks(markerDir)
	if err != nil {
		return nil, err
	}
	t := &tracker{
		store:        projectStore,
		root:         projectStore.Root(),
		markerDir:    markerDir,
		dirty:        map[string]bool{},
		shouldRescan: true,
		markers:      map[string]chan fsnotify.EventID{},
		historyDone:  make(chan struct{}),
	}
	since := t.resume()
	if since == fsnotify.SinceNow {
		close(t.historyDone)
	}
	if err := t.startStream(since); err != nil {
		return nil, err
	}
	return t, nil
}

// startStream reads the exclude rules and starts watching with them. On Linux
// the stream does not watch excluded directories, so it has to start over
// when the rules change.
func (t *tracker) startStream(since fsnotify.EventID) error {
	matcher, err := exclude.New(t.root)
	if err != nil {
		return err
	}
	previous := t.matcher.Swap(matcher)
	stream, err := fsnotify.NewStream([]string{t.root, t.markerDir}, since, t.shouldSkip, t.handleEvents)
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
	t.listing, t.lastID = listing, id
	t.hardLinked = hardLinkedFiles(listing)
	t.unreadable, t.unsavedFiles = nil, nil
	for _, rel := range manifest.Unreadable {
		if listing[rel].Kind == tree.Dir {
			t.unreadable = append(t.unreadable, rel)
		} else {
			t.unsavedFiles = append(t.unsavedFiles, rel)
		}
	}
	return nil
}

// Close stops watching and saves the listing, so the next tracker or scan
// does not have to rebuild it from the layers.
// Closing it again does nothing.
func (t *tracker) Close() {
	t.closeOnce.Do(func() {
		t.stream.Close()
		t.saveListing()
		t.savePosition()
	})
}

// SaveState saves where a new tracker can resume, and the listing when the
// saved one is far behind. The server calls it when it goes quiet, so a
// watcher that is killed still leaves a recent position.
func (t *tracker) SaveState() {
	if t.store.IsListingBehind(t.lastID) {
		t.saveListing()
	}
	t.savePosition()
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
		if event.IsHistoryDone {
			t.closeHistoryDone()
			continue
		}
		if done, ok := t.markers[event.Path]; ok {
			done <- event.ID
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

// closeHistoryDone marks the replayed history as delivered. The caller holds
// t.mu.
func (t *tracker) closeHistoryDone() {
	select {
	case <-t.historyDone:
	default:
		close(t.historyDone)
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

// Checkpoint records the project's current state. Before that, it waits until
// every change made before the call has been delivered.
func (t *tracker) Checkpoint(options store.CheckpointOptions) (store.Checkpoint, error) {
	markerID, isDelivered := t.waitForMarker()
	t.mu.Lock()
	dirty, shouldRescan, hasRulesChange := t.dirty, t.shouldRescan || !isDelivered, t.hasRulesChange
	t.dirty, t.shouldRescan, t.hasRulesChange = map[string]bool{}, false, false
	t.mu.Unlock()

	if hasRulesChange {
		// Paths the old rules excluded were never tracked, and a restore would
		// treat them as created after the checkpoint.
		t.stream.Close()
		if err := t.startStream(fsnotify.SinceNow); err != nil {
			return store.Checkpoint{}, fmt.Errorf("%w: %w", errStreamStopped, err)
		}
		shouldRescan = true
	}
	checkpoint, err := t.checkpoint(options, dirty, shouldRescan)
	if err != nil {
		t.mu.Lock()
		t.shouldRescan = true
		t.mu.Unlock()
		t.listing = nil
		return checkpoint, err
	}
	if isDelivered {
		t.position = streamPosition{CheckpointID: checkpoint.ID, EventID: uint64(markerID)}
	}
	return checkpoint, nil
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
		for _, rel := range t.unsavedFiles {
			markDirty(dirty, t.path(rel))
		}
		for rel := range t.hardLinked {
			markDirty(dirty, t.path(rel))
		}
		changed, deleted, err = t.applyDirty(dirty)
		if errors.Is(err, fs.ErrPermission) {
			shouldRescan = true
		} else if err != nil {
			return store.Checkpoint{}, err
		}
		t.unreadable = slices.DeleteFunc(t.unreadable, t.isReadableOrGone)
		t.updateHardLinked(changed, deleted)
	}
	if shouldRescan {
		current, skipped, err := tree.Scan(t.root, t.matcher.Load())
		if err != nil {
			return store.Checkpoint{}, err
		}
		changed, deleted = tree.Changed(t.listing, current)
		t.listing, t.unreadable = current, skipped.Unreadable
		t.hardLinked = hardLinkedFiles(current)
	}

	checkpoint, err := t.store.Commit(latest.ID, options, store.Changes{
		Listing:         t.listing,
		Changed:         changed,
		Deleted:         deleted,
		Unreadable:      t.unreadable,
		ExcludePatterns: t.matcher.Load().Patterns(),
	})
	if errors.Is(err, store.ErrStale) {
		t.listing = nil
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

// hardLinkedFiles returns the files in listing with more than one name.
func hardLinkedFiles(listing tree.Listing) map[string]bool {
	hardLinked := map[string]bool{}
	for rel, entry := range listing {
		if entry.Links > 1 {
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
		if t.listing[rel].Links > 1 {
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

// isReadableOrGone reports whether the directory rel can be listed now, or
// no longer exists.
func (t *tracker) isReadableOrGone(rel string) bool {
	err := unix.Access(t.path(rel), unix.R_OK|unix.X_OK)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// waitForMarker writes a file next to the store and waits for its event.
// Events arrive in order, so once this one arrives, every earlier change has
// arrived too.
func (t *tracker) waitForMarker() (fsnotify.EventID, bool) {
	// A resumed stream can deliver the marker before the earlier changes.
	select {
	case <-t.historyDone:
	case <-time.After(markerTimeout):
		// Waiting again on every checkpoint would make each one slow. This
		// checkpoint compares the whole project instead, and later replayed
		// events only mark paths to check again.
		t.mu.Lock()
		t.closeHistoryDone()
		t.mu.Unlock()
		return 0, false
	}
	markerPath := filepath.Join(t.markerDir, fmt.Sprintf("%d-%d", os.Getpid(), t.sequence.Add(1)))
	done := make(chan fsnotify.EventID, 1)
	t.mu.Lock()
	t.markers[markerPath] = done
	t.mu.Unlock()
	defer os.Remove(markerPath)

	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return 0, false
	}
	if err := marker.Close(); err != nil {
		return 0, false
	}
	select {
	case id := <-done:
		return id, true
	case <-time.After(markerTimeout):
		t.mu.Lock()
		delete(t.markers, markerPath)
		t.mu.Unlock()
		return 0, false
	}
}

// applyDirty updates the listing for every path reported since the last
// checkpoint and returns what changed. A path it has no permission to read
// returns an error matching fs.ErrPermission, and the caller compares the
// whole project instead, as a scan handles unreadable paths.
func (t *tracker) applyDirty(dirty map[string]bool) (changed, deleted []string, err error) {
	changes := newChangeSet(t.listing)
	matcher := t.matcher.Load()
	parents := tree.NewParentChecker(t.root)
	for fullPath, isRenamed := range t.withHardLinks(dirty) {
		if err := t.applyPath(changes, matcher, parents, fullPath, isRenamed); err != nil {
			return nil, nil, err
		}
	}
	changes.finish()
	return slices.Sorted(maps.Keys(changes.changed)), slices.Sorted(maps.Keys(changes.deleted)), nil
}

// withHardLinks adds the other names of every dirty file that has several.
// A write through one name changes the content of all of them, but the
// event names only the one that was written.
func (t *tracker) withHardLinks(dirty map[string]bool) map[string]bool {
	var inodes map[uint64]bool
	for fullPath := range dirty {
		info, err := os.Lstat(fullPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			if inodes == nil {
				inodes = map[uint64]bool{}
			}
			inodes[stat.Ino] = true
		}
	}
	if inodes == nil {
		return dirty
	}
	for rel, entry := range t.listing {
		if entry.Kind == tree.File && inodes[entry.Inode] {
			markDirty(dirty, t.path(rel))
		}
	}
	return dirty
}

// applyPath updates the listing for one reported path. A path below a
// symlink or a file is gone from the project, even if a stat through the
// link would find something.
func (t *tracker) applyPath(changes *changeSet, matcher *exclude.Matcher, parents *tree.ParentChecker, fullPath string, isRenamed bool) error {
	rel, err := filepath.Rel(t.root, fullPath)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	previous, wasTracked := changes.listing[rel]
	wasDir := wasTracked && previous.Kind == tree.Dir
	removeRel := func() {
		changes.remove(rel)
		if wasDir {
			changes.removeUnder(rel)
		}
	}
	if err := parents.Check(rel); errors.Is(err, tree.ErrParentNotDir) {
		removeRel()
		return nil
	} else if err != nil {
		return err
	}
	entry, err := tree.Stat(fullPath)
	if errors.Is(err, fs.ErrNotExist) {
		removeRel()
		return nil
	}
	if err != nil {
		return err
	}
	storedName, err := tree.StoredName(fullPath)
	if errors.Is(err, fs.ErrNotExist) {
		removeRel()
		return nil
	}
	if err != nil {
		return err
	}
	if storedName != filepath.Base(fullPath) {
		// The event names the path before a rename that changed only case or
		// Unicode form, and the stat reached the renamed file.
		removeRel()
		return t.applyPath(changes, matcher, parents, filepath.Join(filepath.Dir(fullPath), storedName), isRenamed)
	}
	if matcher.Excludes(rel, entry.Kind == tree.Dir) {
		return nil
	}
	// A directory renamed into place, even the one that was here before, can
	// hold files that changed elsewhere and have no events of their own.
	isReplaced := wasDir && (entry.Kind != tree.Dir || previous.Inode != entry.Inode || isRenamed)
	if isReplaced {
		changes.replaceUnder(rel)
	}
	changes.update(rel, entry)
	// A directory that is new or replaced, or whose mode changed and may have
	// become readable, can hold files that never had events of their own.
	if entry.Kind == tree.Dir && (!wasDir || isReplaced || previous.Mode != entry.Mode) {
		return t.addUnder(changes, matcher, fullPath)
	}
	return nil
}

// addUnder adds everything inside a new directory to the listing, in case some
// of its files were created before their events arrived.
func (t *tracker) addUnder(changes *changeSet, matcher *exclude.Matcher, dir string) error {
	return filepath.WalkDir(dir, func(fullPath string, dirEntry fs.DirEntry, err error) error {
		if err != nil || fullPath == dir {
			return nil
		}
		rel, err := filepath.Rel(t.root, fullPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if matcher.Match(rel, dirEntry.IsDir()) {
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry, err := tree.Stat(fullPath)
		if err != nil {
			return nil
		}
		changes.update(rel, entry)
		return nil
	})
}
