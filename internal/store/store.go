// Package store keeps the checkpoints of a project.
package store

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/shimlabs/undo/internal/project"
)

// Store is the data undo keeps on disk for one project.
type Store struct {
	root string
	dir  string
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
	return &Store{root: root, dir: dir}, nil
}

// Root returns the project directory this store protects.
func (s *Store) Root() string { return s.root }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }
