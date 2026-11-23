package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/tree"
)

// Source is the state of a path at a checkpoint and where its content is
// stored: in the base when Layer is 0, or in that checkpoint's layer.
type Source struct {
	Entry tree.Entry
	Layer int
}

// ErrMergePending means an earlier merge of old checkpoints did not finish,
// so the stored states are not consistent yet. The next checkpoint retries
// the merge, and the error log says why it failed.
var ErrMergePending = errors.New("an earlier cleanup of old checkpoints has not finished")

// State returns every path of the project as it was at checkpoint id. It
// starts from the base and applies each layer up to id.
func (s *Store) State(id int) (map[string]Source, error) {
	if _, err := os.Stat(s.mergePath()); err == nil {
		return nil, ErrMergePending
	}
	if _, err := s.Checkpoint(id); err != nil {
		return nil, err
	}
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	base, err := s.BaseListing()
	if err != nil {
		return nil, err
	}
	state := make(map[string]Source, len(base))
	for rel, entry := range base {
		state[rel] = Source{Entry: entry}
	}
	for _, cp := range list {
		if cp.ID > id {
			break
		}
		manifest, err := s.Manifest(cp.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(state, rel)
		}
		for rel, entry := range manifest.Changed {
			state[rel] = Source{Entry: entry, Layer: cp.ID}
		}
	}
	return state, nil
}

// Manifest returns the manifest of checkpoint id.
func (s *Store) Manifest(id int) (Manifest, error) {
	var manifest Manifest
	err := readJSON(filepath.Join(s.layerDir(id), "manifest.json"), &manifest)
	return manifest, err
}

// Extract writes the stored copy of the file or symlink rel, as recorded in
// source, to dst. An existing dst is replaced in one rename, so it is never
// left half written.
func (s *Store) Extract(rel string, source Source, dst string) error {
	src := s.basePath(rel)
	if source.Layer > 0 {
		src = s.layerFile(source.Layer, rel)
	}
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return s.clone(src, dst)
	}
	tempPath := atomicfile.TempPath(dst)
	if err := s.clone(src, tempPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, dst); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

// ExtractBaseDir writes the whole directory rel from the base to dst, which
// must not exist, in one clone. Only use it when nothing inside rel changed
// since the base.
func (s *Store) ExtractBaseDir(rel, dst string) error {
	return s.clone(s.basePath(rel), dst)
}

func (s *Store) baseDir() string {
	return filepath.Join(s.dir, "base")
}

func (s *Store) basePath(rel string) string {
	if rel == "." {
		return s.baseDir()
	}
	return filepath.Join(s.baseDir(), filepath.FromSlash(rel))
}

func (s *Store) layerDir(id int) string {
	return filepath.Join(s.dir, "layers", strconv.Itoa(id))
}

func (s *Store) layerFile(id int, rel string) string {
	return filepath.Join(s.layerDir(id), "files", filepath.FromSlash(rel))
}
