package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/tree"
)

// Checkpoint is a point the project can be restored to: its state just before
// the command in Label ran.
type Checkpoint struct {
	ID    int       `json:"id"`
	Time  time.Time `json:"time"`
	Label string    `json:"label"`
	Dir   string    `json:"dir"`
}

// Manifest is what a layer recorded: the state of every path that changed
// since the previous checkpoint, and the paths that were deleted.
type Manifest struct {
	Changed  map[string]tree.Entry `json:"changed"`
	Deleted  []string              `json:"deleted"`
	Unstable []string              `json:"unstable,omitempty"`
}

// List returns the checkpoints in the order they were taken.
func (s *Store) List() ([]Checkpoint, error) {
	var list []Checkpoint
	err := readJSON(filepath.Join(s.dir, "index.json"), &list)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return list, err
}

// Layer returns the manifest of checkpoint id.
func (s *Store) Layer(id int) (Manifest, error) {
	var m Manifest
	err := readJSON(filepath.Join(s.layerDir(id), "manifest.json"), &m)
	return m, err
}

func (s *Store) layerDir(id int) string {
	return filepath.Join(s.dir, "layers", strconv.Itoa(id))
}

// BaseDir returns the directory that holds the base clone of the project.
func (s *Store) BaseDir() string {
	return filepath.Join(s.dir, "base")
}

// LayerFile returns the path where checkpoint id keeps its copy of rel.
func (s *Store) LayerFile(id int, rel string) string {
	return filepath.Join(s.layerDir(id), "files", filepath.FromSlash(rel))
}

func (s *Store) lock() (unlock func(), err error) {
	unlock, err = s.flock(unix.LOCK_EX)
	if err != nil {
		return nil, err
	}
	if err := s.finishMerge(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// RLock blocks changes to stored checkpoints until unlock is called. Taking a
// checkpoint or pruning waits for it, but other readers do not.
func (s *Store) RLock() (unlock func(), err error) {
	return s.flock(unix.LOCK_SH)
}

func (s *Store) flock(how int) (unlock func(), err error) {
	file, err := os.OpenFile(filepath.Join(s.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), how); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
	}, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
