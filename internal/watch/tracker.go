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
	stream    *Stream
	sequence  atomic.Uint64

	mu      sync.Mutex
	dirty   map[string]bool
	full    bool
	markers map[string]chan struct{}

	listing tree.Listing
	lastID  int
}

// NewTracker starts watching the project of s. Its first checkpoint compares
// the whole project, because changes made before it started are not known.
func NewTracker(s *store.Store) (*Tracker, error) {
	markerDir := filepath.Join(s.Dir(), "markers")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return nil, err
	}
	markerDir, err := filepath.EvalSymlinks(markerDir)
	if err != nil {
		return nil, err
	}
	t := &Tracker{
		store:     s,
		root:      s.Root(),
		markerDir: markerDir,
		dirty:     map[string]bool{},
		full:      true,
		markers:   map[string]chan struct{}{},
	}
	if t.stream, err = Start([]string{t.root, markerDir}, t.handle); err != nil {
		return nil, err
	}
	return t, nil
}

// Close stops watching.
func (t *Tracker) Close() {
	t.stream.Close()
}

func (t *Tracker) handle(events []Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range events {
		if done, ok := t.markers[e.Path]; ok {
			close(done)
			delete(t.markers, e.Path)
			continue
		}
		if e.Rescan {
			t.full = true
		}
		if e.Path == t.root || strings.HasPrefix(e.Path, t.root+string(filepath.Separator)) {
			t.dirty[e.Path] = true
		}
		// Creating, removing or renaming a path also changes its directory's
		// times, but the event names only the path itself.
		if parent := filepath.Dir(e.Path); parent == t.root || strings.HasPrefix(parent, t.root+string(filepath.Separator)) {
			t.dirty[parent] = true
		}
	}
}

// Checkpoint records the project's current state. Before that, it waits until
// every change made before the call has been delivered.
func (t *Tracker) Checkpoint(label, dir string) (store.Checkpoint, error) {
	if !t.waitForMarker() {
		t.mu.Lock()
		t.full = true
		t.mu.Unlock()
	}
	t.mu.Lock()
	dirty, full := t.dirty, t.full
	t.dirty, t.full = map[string]bool{}, false
	t.mu.Unlock()

	cp, err := t.checkpoint(label, dir, dirty, full)
	if err != nil {
		t.mu.Lock()
		t.full = true
		t.mu.Unlock()
		t.listing = nil
	}
	return cp, err
}

func (t *Tracker) checkpoint(label, dir string, dirty map[string]bool, full bool) (store.Checkpoint, error) {
	list, err := t.store.List()
	if err != nil {
		return store.Checkpoint{}, err
	}
	if len(list) == 0 {
		return t.store.Take(label, dir)
	}
	last := list[len(list)-1].ID
	if t.listing == nil || last != t.lastID {
		if t.listing, err = t.store.ListingAt(last); err != nil {
			return store.Checkpoint{}, err
		}
		t.lastID = last
		full = true
	}
	ex, err := exclude.New(t.root)
	if err != nil {
		return store.Checkpoint{}, err
	}

	var changed, deleted []string
	if full {
		current, err := tree.Scan(t.root, ex)
		if err != nil {
			return store.Checkpoint{}, err
		}
		changed, deleted = tree.Changed(t.listing, current)
		t.listing = current
	} else if changed, deleted, err = t.apply(dirty, ex); err != nil {
		return store.Checkpoint{}, err
	}

	cp, err := t.store.Commit(last, label, dir, t.listing, changed, deleted)
	if errors.Is(err, store.ErrStale) {
		t.listing = nil
		return t.checkpoint(label, dir, nil, true)
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

// apply updates the listing for every path reported since the last
// checkpoint and returns what changed.
func (t *Tracker) apply(dirty map[string]bool, ex *exclude.Matcher) (changed, deleted []string, err error) {
	changedSet, deletedSet := map[string]bool{}, map[string]bool{}
	remove := func(rel string) {
		if _, ok := t.listing[rel]; ok {
			delete(t.listing, rel)
			deletedSet[rel] = true
			delete(changedSet, rel)
		}
	}
	update := func(rel string, entry tree.Entry) {
		if previous, ok := t.listing[rel]; !ok || previous != entry {
			t.listing[rel] = entry
			changedSet[rel] = true
			delete(deletedSet, rel)
		}
	}

	for path := range dirty {
		rel, err := filepath.Rel(t.root, path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if excluded(ex, rel) {
			continue
		}
		previous, existed := t.listing[rel]
		entry, err := tree.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			remove(rel)
			if existed && previous.Kind == tree.Dir {
				t.removeUnder(rel, remove)
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if existed && previous.Kind == tree.Dir && entry.Kind != tree.Dir {
			t.removeUnder(rel, remove)
		}
		update(rel, entry)
		if entry.Kind == tree.Dir && (!existed || previous.Kind != tree.Dir) {
			if err := t.addUnder(path, ex, update); err != nil {
				return nil, nil, err
			}
		}
	}
	for rel := range changedSet {
		changed = append(changed, rel)
	}
	for rel := range deletedSet {
		deleted = append(deleted, rel)
	}
	slices.Sort(changed)
	slices.Sort(deleted)
	return changed, deleted, nil
}

func (t *Tracker) removeUnder(rel string, remove func(string)) {
	prefix := rel + "/"
	if rel == "." {
		prefix = ""
	}
	for path := range t.listing {
		if path != "." && strings.HasPrefix(path, prefix) {
			remove(path)
		}
	}
}

// addUnder adds everything inside a new directory to the listing, in case some
// of its files were created before their events arrived.
func (t *Tracker) addUnder(dir string, ex *exclude.Matcher, update func(string, tree.Entry)) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		rel, err := filepath.Rel(t.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ex.Match(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry, err := tree.Stat(path)
		if err != nil {
			return nil
		}
		update(rel, entry)
		return nil
	})
}

// excluded reports whether rel, or any directory above it, is excluded.
func excluded(ex *exclude.Matcher, rel string) bool {
	if rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		if ex.Match(strings.Join(parts[:i+1], "/"), i < len(parts)-1) {
			return true
		}
	}
	return false
}
