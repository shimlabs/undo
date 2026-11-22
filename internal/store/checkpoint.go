package store

import (
	"bytes"
	"encoding/gob"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/tree"
)

const cloneAttempts = 3

// Take records the project's current state as a new checkpoint. The first
// checkpoint clones the whole project into the base. Later checkpoints clone
// only the paths that changed since the previous one.
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

	id := 1
	var previous tree.Listing
	if len(list) > 0 {
		id = list[len(list)-1].ID + 1
		if previous, err = s.Listing(); err != nil {
			return Checkpoint{}, err
		}
	} else if previous, err = s.cloneBase(ex); err != nil {
		return Checkpoint{}, err
	}

	current, err := tree.Scan(s.root, ex)
	if err != nil {
		return Checkpoint{}, err
	}
	changed, deleted := tree.Changed(previous, current)
	manifest, err := s.cloneChanged(id, changed, current)
	if err != nil {
		return Checkpoint{}, err
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
	if err := s.saveListing(current); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

// cloneBase clones the project into the base and returns the listing taken
// just before the clone. If a path changes while the clone runs, it no longer
// matches that listing, so the first checkpoint records it again.
func (s *Store) cloneBase(ex *exclude.Matcher) (tree.Listing, error) {
	before, err := tree.Scan(s.root, ex)
	if err != nil {
		return nil, err
	}
	base := s.BaseDir()
	if err := os.RemoveAll(base); err != nil {
		return nil, err
	}
	if err := clone.Tree(s.root, base); err != nil {
		return nil, err
	}
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == "." {
			return err
		}
		if !ex.Match(filepath.ToSlash(rel), d.IsDir()) {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return before, err
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
		if err := clone.File(src, dst); err != nil {
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

// Listing returns the project listing saved at the latest checkpoint.
func (s *Store) Listing() (tree.Listing, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "tree.gob"))
	if err != nil {
		return nil, err
	}
	var listing tree.Listing
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(&listing)
	return listing, err
}

func (s *Store) saveListing(listing tree.Listing) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(listing); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "tree.gob"), buf.Bytes())
}
