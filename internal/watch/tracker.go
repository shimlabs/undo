// Package watch runs a background process for each project that follows its
// files as they change, so a checkpoint only has to look at the paths that
// changed instead of scanning the whole project.
package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

	mu             sync.Mutex
	dirty          map[string]bool
	needsFullScan  bool
	isRulesChanged bool
	markers        map[string]chan struct{}

	listing    tree.Listing
	unreadable []string
	lastID     int
}

// NewTracker starts watching the project of s. Its first checkpoint compares
// the whole project, because changes made before it started are not known.
func NewTracker(s *store.Store) (*Tracker, error) {
	markerDir := s.FilePath("markers")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return nil, err
	}
	markerDir, err := filepath.EvalSymlinks(markerDir)
	if err != nil {
		return nil, err
	}
	t := &Tracker{
		store:         s,
		root:          s.Root(),
		markerDir:     markerDir,
		dirty:         map[string]bool{},
		needsFullScan: true,
		markers:       map[string]chan struct{}{},
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

// Close stops watching.
func (t *Tracker) Close() {
	t.stream.Close()
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
			t.needsFullScan = true
		}
		if event.Path == filepath.Join(t.root, ".undoignore") {
			t.isRulesChanged = true
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
	dirty, needsFullScan, isRulesChanged := t.dirty, t.needsFullScan || !isDelivered, t.isRulesChanged
	t.dirty, t.needsFullScan, t.isRulesChanged = map[string]bool{}, false, false
	t.mu.Unlock()

	if isRulesChanged {
		// Paths the old rules excluded were never tracked, and a restore would
		// treat them as created after the checkpoint.
		t.stream.Close()
		if err := t.startStream(); err != nil {
			return store.Checkpoint{}, err
		}
		needsFullScan = true
	}
	cp, err := t.checkpoint(options, dirty, needsFullScan)
	if err != nil {
		t.mu.Lock()
		t.needsFullScan = true
		t.mu.Unlock()
		t.listing = nil
	}
	return cp, err
}

func (t *Tracker) checkpoint(options store.CheckpointOptions, dirty map[string]bool, needsFullScan bool) (store.Checkpoint, error) {
	latest, hasLatest, err := t.store.Latest()
	if err != nil {
		return store.Checkpoint{}, err
	}
	if !hasLatest {
		cp, err := t.store.Take(options)
		if err != nil {
			return cp, err
		}
		// Take saved the listing it scanned, and every change since then is in
		// dirty, so the next checkpoint does not need a full scan.
		t.listing, err = t.store.ListingAt(cp.ID)
		t.lastID = cp.ID
		return cp, err
	}
	if t.listing == nil || latest.ID != t.lastID {
		if t.listing, err = t.store.ListingAt(latest.ID); err != nil {
			return store.Checkpoint{}, err
		}
		t.lastID = latest.ID
		needsFullScan = true
	}

	var changed, deleted []string
	if needsFullScan {
		current, skipped, err := tree.ScanWithSkipped(t.root, t.matcher.Load())
		if err != nil {
			return store.Checkpoint{}, err
		}
		changed, deleted = tree.Changed(t.listing, current)
		t.listing, t.unreadable = current, skipped.Unreadable
	} else if changed, deleted, err = t.applyDirty(dirty); err != nil {
		return store.Checkpoint{}, err
	}

	cp, err := t.store.Commit(latest.ID, options, store.Changes{
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
	t.lastID = cp.ID
	return cp, nil
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
// checkpoint and returns what changed.
func (t *Tracker) applyDirty(dirty map[string]bool) (changed, deleted []string, err error) {
	changes := changeSet{listing: t.listing, changed: map[string]bool{}, deleted: map[string]bool{}}
	matcher := t.matcher.Load()
	for path := range dirty {
		if err := t.applyPath(&changes, matcher, path); err != nil {
			return nil, nil, err
		}
	}
	changed = slices.Sorted(func(yield func(string) bool) {
		for rel := range changes.changed {
			if !yield(rel) {
				return
			}
		}
	})
	deleted = slices.Sorted(func(yield func(string) bool) {
		for rel := range changes.deleted {
			if !yield(rel) {
				return
			}
		}
	})
	return changed, deleted, nil
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
	if entry.Kind == tree.Dir && (!existed || previous.Kind != tree.Dir) {
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
		stat, err := tree.Stat(path)
		if err != nil {
			return nil
		}
		changes.update(rel, stat)
		return nil
	})
}
