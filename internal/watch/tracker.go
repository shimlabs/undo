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
)

// markerTimeout is how long a checkpoint waits for its marker event. After
// that, the tracker stops trusting its events and compares the whole project
// instead.
const markerTimeout = 3 * time.Second

// Tracker keeps a project's listing in memory and updates it from filesystem
// events, so a checkpoint only has to check the paths that changed.
type Tracker struct {
	store     *store.Store
	root      string
	markerDir string
	matcher   atomic.Pointer[exclude.Matcher]
	stream    *fsnotify.Stream
	sequence  atomic.Uint64
	closeOnce sync.Once

	mu             sync.Mutex
	dirty          map[string]bool
	shouldRescan   bool
	hasRulesChange bool
	markers        map[string]chan struct{}

	listing    tree.Listing
	unreadable []string
	lastID     int
}

// NewTracker starts watching the project of s. Its first checkpoint compares
// the whole project, because changes made before it started are not known.
func NewTracker(projectStore *store.Store) (*Tracker, error) {
	markerDir := projectStore.MarkerDir()
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return nil, err
	}
	markerDir, err := filepath.EvalSymlinks(markerDir)
	if err != nil {
		return nil, err
	}
	t := &Tracker{
		store:        projectStore,
		root:         projectStore.Root(),
		markerDir:    markerDir,
		dirty:        map[string]bool{},
		shouldRescan: true,
		markers:      map[string]chan struct{}{},
	}
	if err := t.startStream(); err != nil {
		return nil, err
	}
	return t, nil
}

// startStream reads the exclude rules and starts watching with them. On Linux
// the stream does not watch excluded directories, so it has to start over
// when the rules change.
func (t *Tracker) startStream() error {
	matcher, err := exclude.New(t.root)
	if err != nil {
		return err
	}
	t.matcher.Store(matcher)
	stream, err := fsnotify.NewStream([]string{t.root, t.markerDir}, t.shouldSkip, t.handleEvents)
	if err != nil {
		return err
	}
	t.stream = stream
	return nil
}

// Close stops watching and saves the listing, so the next tracker or scan
// does not have to rebuild it from the layers.
// Closing it again does nothing.
func (t *Tracker) Close() {
	t.closeOnce.Do(func() {
		t.stream.Close()
		t.SaveListing()
	})
}

// SaveListing saves the tracker's listing in the store. The server calls it
// when it goes quiet.
func (t *Tracker) SaveListing() {
	if t.listing != nil && t.lastID > 0 {
		if err := t.store.SaveListing(t.lastID, t.listing); err != nil {
			t.store.LogError(fmt.Errorf("saving the listing: %w", err))
		}
	}
}

// shouldSkip reports whether dir is excluded, so the stream does not need to
// watch it.
func (t *Tracker) shouldSkip(dir string) bool {
	rel, err := filepath.Rel(t.root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return t.matcher.Load().Excludes(filepath.ToSlash(rel), true)
}

func (t *Tracker) handleEvents(events []fsnotify.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, event := range events {
		if done, ok := t.markers[event.Path]; ok {
			close(done)
			delete(t.markers, event.Path)
			continue
		}
		if event.ShouldRescan {
			t.shouldRescan = true
		}
		if event.Path == filepath.Join(t.root, ".undoignore") {
			t.hasRulesChange = true
		}
		if t.isInRoot(event.Path) {
			t.dirty[event.Path] = true
		}
		// Creating, removing or renaming a path also changes its directory's
		// times, but the event names only the path itself.
		if parent := filepath.Dir(event.Path); t.isInRoot(parent) {
			t.dirty[parent] = true
		}
	}
}

func (t *Tracker) isInRoot(path string) bool {
	return path == t.root || strings.HasPrefix(path, t.root+string(filepath.Separator))
}

// Checkpoint records the project's current state. Before that, it waits until
// every change made before the call has been delivered.
func (t *Tracker) Checkpoint(options store.CheckpointOptions) (store.Checkpoint, error) {
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
			return store.Checkpoint{}, err
		}
		shouldRescan = true
	}
	checkpoint, err := t.checkpoint(options, dirty, shouldRescan)
	if err != nil {
		t.mu.Lock()
		t.shouldRescan = true
		t.mu.Unlock()
		t.listing = nil
	}
	return checkpoint, err
}

func (t *Tracker) checkpoint(options store.CheckpointOptions, dirty map[string]bool, shouldRescan bool) (store.Checkpoint, error) {
	latest, hasLatest, err := t.store.Latest()
	if err != nil {
		return store.Checkpoint{}, err
	}
	if !hasLatest {
		checkpoint, err := t.store.Take(options)
		if err != nil {
			return checkpoint, err
		}
		// Take saved the listing it scanned, and every change since then is in
		// dirty, so the next checkpoint does not need a full scan.
		t.listing, err = t.store.ListingAt(checkpoint.ID)
		t.lastID = checkpoint.ID
		return checkpoint, err
	}
	if t.listing == nil || latest.ID != t.lastID {
		if t.listing, err = t.store.ListingAt(latest.ID); err != nil {
			return store.Checkpoint{}, err
		}
		t.lastID = latest.ID
		shouldRescan = true
	}

	var changed, deleted []string
	if !shouldRescan {
		changed, deleted, err = t.applyDirty(dirty)
		if errors.Is(err, fs.ErrPermission) {
			shouldRescan = true
		} else if err != nil {
			return store.Checkpoint{}, err
		}
	}
	if shouldRescan {
		current, skipped, err := tree.Scan(t.root, t.matcher.Load())
		if err != nil {
			return store.Checkpoint{}, err
		}
		changed, deleted = tree.Changed(t.listing, current)
		t.listing, t.unreadable = current, skipped.Unreadable
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
	return checkpoint, nil
}

// waitForMarker writes a file next to the store and waits for its event.
// Events arrive in order, so once this one arrives, every earlier change has
// arrived too.
func (t *Tracker) waitForMarker() bool {
	path := filepath.Join(t.markerDir, fmt.Sprintf("%d-%d", os.Getpid(), t.sequence.Add(1)))
	done := make(chan struct{})
	t.mu.Lock()
	t.markers[path] = done
	t.mu.Unlock()
	defer os.Remove(path)

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(markerTimeout):
		t.mu.Lock()
		delete(t.markers, path)
		t.mu.Unlock()
		return false
	}
}

// changeSet collects the paths a checkpoint found changed or deleted while it
// updates the listing.
type changeSet struct {
	listing tree.Listing
	changed map[string]bool
	deleted map[string]bool
}

func (c *changeSet) remove(rel string) {
	if _, ok := c.listing[rel]; ok {
		delete(c.listing, rel)
		c.deleted[rel] = true
		delete(c.changed, rel)
	}
}

func (c *changeSet) update(rel string, entry tree.Entry) {
	if previous, ok := c.listing[rel]; !ok || previous != entry {
		c.listing[rel] = entry
		c.changed[rel] = true
		delete(c.deleted, rel)
	}
}

func (c *changeSet) removeUnder(rel string) {
	prefix := rel + "/"
	if rel == "." {
		prefix = ""
	}
	for path := range c.listing {
		if path != "." && strings.HasPrefix(path, prefix) {
			c.remove(path)
		}
	}
}

// applyDirty updates the listing for every path reported since the last
// checkpoint and returns what changed. A path it has no permission to read
// returns an error matching fs.ErrPermission, and the caller compares the
// whole project instead, as a scan handles unreadable paths.
func (t *Tracker) applyDirty(dirty map[string]bool) (changed, deleted []string, err error) {
	changes := changeSet{listing: t.listing, changed: map[string]bool{}, deleted: map[string]bool{}}
	matcher := t.matcher.Load()
	for path := range t.withHardLinks(dirty) {
		if err := t.applyPath(&changes, matcher, path); err != nil {
			return nil, nil, err
		}
	}
	return slices.Sorted(maps.Keys(changes.changed)), slices.Sorted(maps.Keys(changes.deleted)), nil
}

// withHardLinks adds the other names of every dirty file that has several.
// A write through one name changes the content of all of them, but the
// event names only the one that was written.
func (t *Tracker) withHardLinks(dirty map[string]bool) map[string]bool {
	var inodes map[uint64]bool
	for path := range dirty {
		info, err := os.Lstat(path)
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
			dirty[filepath.Join(t.root, filepath.FromSlash(rel))] = true
		}
	}
	return dirty
}

func (t *Tracker) applyPath(changes *changeSet, matcher *exclude.Matcher, path string) error {
	rel, err := filepath.Rel(t.root, path)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	previous, existed := changes.listing[rel]
	entry, err := tree.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		changes.remove(rel)
		if existed && previous.Kind == tree.Dir {
			changes.removeUnder(rel)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if matcher.Excludes(rel, entry.Kind == tree.Dir) {
		return nil
	}
	if existed && previous.Kind == tree.Dir && entry.Kind != tree.Dir {
		changes.removeUnder(rel)
	}
	changes.update(rel, entry)
	// A directory that is new, or whose mode changed and may have become
	// readable, can hold files that never had events of their own.
	isNewDir := !existed || previous.Kind != tree.Dir
	if entry.Kind == tree.Dir && (isNewDir || previous.Mode != entry.Mode) {
		return t.addUnder(changes, matcher, path)
	}
	return nil
}

// addUnder adds everything inside a new directory to the listing, in case some
// of its files were created before their events arrived.
func (t *Tracker) addUnder(changes *changeSet, matcher *exclude.Matcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		rel, err := filepath.Rel(t.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if matcher.Match(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		treeEntry, err := tree.Stat(path)
		if err != nil {
			return nil
		}
		changes.update(rel, treeEntry)
		return nil
	})
}
