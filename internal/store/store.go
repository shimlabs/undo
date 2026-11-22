// Package store keeps the checkpoints of a project.
package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/project"
)

// Store is the data undo keeps on disk for one project.
type Store struct {
	root   string
	dir    string
	config config
}

type config struct {
	Copy bool `json:"copy"`
}

// Home returns the directory that holds the stores of all projects.
func Home() (string, error) {
	if dir := os.Getenv("UNDO_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "undo"), nil
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "undo"), nil
	}
	return filepath.Join(home, ".local", "state", "undo"), nil
}

// Open returns the store for the project at root and creates it if needed.
func Open(root string) (*Store, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, project.ID(root))
	if err := os.MkdirAll(filepath.Join(dir, "layers"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{root: root, dir: dir}
	return s, s.loadConfig()
}

// Root returns the project directory this store protects.
func (s *Store) Root() string { return s.root }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// ErrCloneUnsupported means the project's filesystem cannot clone files into
// the store. Copy mode works there, at the cost of a full copy of the project.
var ErrCloneUnsupported = errors.New("this filesystem cannot make copy-on-write clones")

// CopyMode reports whether the store keeps full copies instead of clones.
func (s *Store) CopyMode() bool { return s.config.Copy }

// SetCopyMode switches between clones and full copies, and saves the choice
// with the store.
func (s *Store) SetCopyMode(on bool) error {
	s.config.Copy = on
	return writeJSON(s.configPath(), s.config)
}

// Clone copies src to dst the way the store keeps files: as a clone, or as a
// full copy in copy mode.
func (s *Store) Clone(src, dst string) error {
	if s.config.Copy {
		return clone.Copy(src, dst)
	}
	return clone.File(src, dst)
}

// loadConfig reads the saved settings. It runs whenever the store is locked,
// so a long-running watcher sees copy mode turned on by another process.
func (s *Store) loadConfig() error {
	s.config = config{}
	if err := readJSON(s.configPath(), &s.config); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) configPath() string {
	return filepath.Join(s.dir, "config.json")
}
