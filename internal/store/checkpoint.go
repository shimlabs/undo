package store

import (
	"bytes"
	"encoding/gob"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/tree"
)

const cloneAttempts = 3

// Take records the project's current state as a new checkpoint. The first
// checkpoint clones the whole project into the base. Later checkpoints scan
// the project and clone only the paths that changed since the previous one.
func (s *Store) Take(label, dir string) (Checkpoint, error) {
	unlock, err := s.lock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer unlock()

	ex, err := exclude.New(s.root)
	if err != nil {
		return Checkpoint{}, err
	}
	list, err := s.List()
	if err != nil {
		return Checkpoint{}, err
	}

	var previous tree.Listing
	if len(list) > 0 {
		if previous, err = s.ListingAt(list[len(list)-1].ID); err != nil {
			return Checkpoint{}, err
		}
	} else {
		if previous, err = s.cloneBase(ex); err != nil {
			os.RemoveAll(s.BaseDir())
			return Checkpoint{}, cloneError(err)
		}
		if err := s.saveGob("base.gob", previous); err != nil {
			return Checkpoint{}, err
		}
	}

	current, err := tree.Scan(s.root, ex)
	if err != nil {
		return Checkpoint{}, err
	}
	changed, deleted := tree.Changed(previous, current)
	cp, err := s.commit(list, label, dir, current, changed, deleted)
	if err != nil {
		return Checkpoint{}, err
	}
	return cp, s.saveListing(cp.ID, current)
}

// ErrStale means another checkpoint was taken after the one the changes were
// based on.
var ErrStale = errors.New("changes are not relative to the latest checkpoint")

// Commit records a checkpoint from changes found by a caller that tracks the
// project itself, such as the watcher. current is the full listing of the
// project. changed and deleted are the paths that differ from the listing at
// checkpoint after, which must be the latest checkpoint.
func (s *Store) Commit(after int, label, dir string, current tree.Listing, changed, deleted []string) (Checkpoint, error) {
	unlock, err := s.lock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer unlock()

	list, err := s.List()
	if err != nil {
		return Checkpoint{}, err
	}
	if len(list) == 0 || list[len(list)-1].ID != after {
		return Checkpoint{}, ErrStale
	}
	return s.commit(list, label, dir, current, changed, deleted)
}

func (s *Store) commit(list []Checkpoint, label, dir string, current tree.Listing, changed, deleted []string) (Checkpoint, error) {
	id := 1
	if len(list) > 0 {
		id = list[len(list)-1].ID + 1
	}
	manifest, err := s.cloneChanged(id, changed, current)
	if err != nil {
		os.RemoveAll(s.layerDir(id))
		return Checkpoint{}, cloneError(err)
	}
	manifest.Deleted = append(manifest.Deleted, deleted...)

	if err := os.MkdirAll(s.layerDir(id), 0o700); err != nil {
		return Checkpoint{}, err
	}
	if err := writeJSON(filepath.Join(s.layerDir(id), "manifest.json"), manifest); err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{ID: id, Time: time.Now(), Label: label, Dir: dir}
	if err := writeJSON(filepath.Join(s.dir, "index.json"), append(list, cp)); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

func cloneError(err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return ErrCloneUnsupported
	}
	return err
}

// cloneBase clones the project into the base and returns the listing taken
// just before the clone. If a path changes while the clone runs, it no longer
// matches that listing, so the first checkpoint records it again. Excluded
// paths are never read.
func (s *Store) cloneBase(ex *exclude.Matcher) (tree.Listing, error) {
	before, excluded, err := tree.ScanAll(s.root, ex)
	if err != nil {
		return nil, err
	}
	base := s.BaseDir()
	if err := os.RemoveAll(base); err != nil {
		return nil, err
	}
	holders := map[string]bool{}
	for _, rel := range excluded {
		for dir := path.Dir(rel); !holders[dir]; dir = path.Dir(dir) {
			holders[dir] = true
			if dir == "." {
				break
			}
		}
	}
	children := map[string][]string{}
	for rel := range before {
		if rel != "." {
			children[path.Dir(rel)] = append(children[path.Dir(rel)], rel)
		}
	}
	return before, s.cloneInto(".", base, holders, children)
}

// cloneInto clones rel to dst in one call. If rel contains excluded paths, it
// creates the directory instead and clones each child separately.
func (s *Store) cloneInto(rel, dst string, holders map[string]bool, children map[string][]string) error {
	src := filepath.Join(s.root, filepath.FromSlash(rel))
	if !holders[rel] {
		err := s.Clone(src, dst)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, child := range children[rel] {
		if err := s.cloneInto(child, filepath.Join(dst, path.Base(child)), holders, children); err != nil {
			return err
		}
	}
	return nil
}

// cloneChanged copies each changed file and symlink into the layer for id. If a
// file is written while it is being cloned, its entry in listing is updated.
func (s *Store) cloneChanged(id int, changed []string, listing tree.Listing) (Manifest, error) {
	manifest := Manifest{Changed: map[string]tree.Entry{}, Deleted: []string{}}
	for _, rel := range changed {
		entry := listing[rel]
		if entry.Kind != tree.File && entry.Kind != tree.Symlink {
			manifest.Changed[rel] = entry
			continue
		}
		entry, stable, err := s.cloneStable(id, rel, entry)
		if errors.Is(err, fs.ErrNotExist) {
			delete(listing, rel)
			manifest.Deleted = append(manifest.Deleted, rel)
			continue
		}
		if err != nil {
			return Manifest{}, err
		}
		if !stable {
			manifest.Unstable = append(manifest.Unstable, rel)
		}
		listing[rel] = entry
		manifest.Changed[rel] = entry
	}
	return manifest, nil
}

// cloneStable clones rel into the layer and checks that the source did not
// change during the clone. If it did, it tries again a few times.
func (s *Store) cloneStable(id int, rel string, entry tree.Entry) (tree.Entry, bool, error) {
	src := filepath.Join(s.root, filepath.FromSlash(rel))
	dst := s.LayerFile(id, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return entry, false, err
	}
	for attempt := 1; ; attempt++ {
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return entry, false, err
		}
		if err := s.Clone(src, dst); err != nil {
			return entry, false, err
		}
		after, err := tree.Stat(src)
		if err != nil {
			return entry, false, err
		}
		if after == entry {
			return entry, true, nil
		}
		entry = after
		if attempt == cloneAttempts {
			return entry, false, nil
		}
	}
}

// BaseListing returns the listing of the project at the moment the base was
// cloned.
func (s *Store) BaseListing() (tree.Listing, error) {
	return s.loadGob("base.gob")
}

type savedListing struct {
	ID      int
	Listing tree.Listing
}

// ListingAt returns the project listing at checkpoint id. It uses the saved
// copy when that matches, and otherwise rebuilds the listing from the base and
// the layers.
func (s *Store) ListingAt(id int) (tree.Listing, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "tree.gob"))
	if err == nil {
		var saved savedListing
		if gob.NewDecoder(bytes.NewReader(data)).Decode(&saved) == nil && saved.ID == id {
			return saved.Listing, nil
		}
	}
	state, err := s.State(id)
	if err != nil {
		return nil, err
	}
	listing := make(tree.Listing, len(state))
	for rel, src := range state {
		listing[rel] = src.Entry
	}
	return listing, nil
}

func (s *Store) saveListing(id int, listing tree.Listing) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(savedListing{ID: id, Listing: listing}); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "tree.gob"), buf.Bytes())
}

func (s *Store) loadGob(name string) (tree.Listing, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	var listing tree.Listing
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(&listing)
	return listing, err
}

func (s *Store) saveGob(name string, listing tree.Listing) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(listing); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, name), buf.Bytes())
}
