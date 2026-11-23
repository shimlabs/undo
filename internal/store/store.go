// Package store keeps the checkpoints of a project: a base clone, one layer of
// changed files per checkpoint, and the listings and index that tie them
// together.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/project"
)

// format is the version of the on-disk layout. A store written with a
// higher number is refused instead of misread.
const format = 1

var (
	// ErrCloneUnsupported means the project's filesystem cannot clone files
	// into the store. Copy mode works there, at the cost of a full copy of the
	// project.
	ErrCloneUnsupported = errors.New("this filesystem cannot make copy-on-write clones")
	// ErrNewerFormat means a newer version of undo wrote the store.
	ErrNewerFormat = errors.New("this store was written by a newer version of undo")
	// ErrUnsafeStore means a store directory exists that is not a private
	// directory of the current user, so someone else could change it.
	ErrUnsafeStore = errors.New("the store directory must be a directory owned by you and private to you")
	// ErrStoreInsideProject means the project holds the store home, and no
	// folder next to it can hold the store instead.
	ErrStoreInsideProject = errors.New("this folder holds the undo store and has no writable parent on the same volume")
)

// Store is the data undo keeps on disk for one project.
type Store struct {
	root   string
	dir    string
	config config
}

type config struct {
	Format     int    `json:"format"`
	Root       string `json:"root"`
	IsCopyMode bool   `json:"copy_mode"`
}

// DefaultHome returns the directory that holds the stores of all projects:
// $UNDO_HOME if it is set, and otherwise the system's place for application
// state.
func DefaultHome() (string, error) {
	if dir := os.Getenv("UNDO_HOME"); dir != "" {
		return dir, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(userHome, "Library", "Application Support", "undo"), nil
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "undo"), nil
	}
	return filepath.Join(userHome, ".local", "state", "undo"), nil
}

// Open returns the existing store of the project at root, whether it lives
// under home or next to the project. It returns an error matching
// fs.ErrNotExist if the project has no store yet, and never creates one.
func Open(home, root string) (*Store, error) {
	for _, dir := range []string{filepath.Join(home, project.ID(root)), nextToProject(root)} {
		if _, err := os.Stat(filepath.Join(dir, configFile)); err != nil {
			continue
		}
		if err := checkPrivateDir(dir); err != nil {
			return nil, err
		}
		return openAt(dir, root)
	}
	return nil, fmt.Errorf("no store for %s: %w", root, fs.ErrNotExist)
}

// Create returns the store of the project at root, creating it if needed.
func Create(home, root string) (*Store, error) {
	if existing, err := Open(home, root); !errors.Is(err, fs.ErrNotExist) {
		return existing, err
	}
	dir, err := locate(home, root)
	if err != nil {
		return nil, err
	}
	return openAt(dir, root)
}

// openAt opens the store in dir for the project at root, writing its
// settings if the store is new.
func openAt(dir, root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, layersDirName), 0o700); err != nil {
		return nil, err
	}
	s := &Store{root: root, dir: dir}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	if s.config.Format > format {
		return nil, fmt.Errorf("%s: %w", dir, ErrNewerFormat)
	}
	if s.config.Format == 0 {
		s.config.Format, s.config.Root = format, root
		if err := writeJSON(s.configPath(), s.config); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// locate returns the directory for a new store of the project at root.
// Stores live under home. Clones only work within one volume, so when the
// project is on another volume, or when home is inside the project, the store
// goes next to the project instead.
func locate(home, root string) (string, error) {
	if err := project.Check(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	id := project.ID(root)
	isInside := isWithin(filepath.Join(resolvedHome, id), root)
	if !isInside && isSameVolume(root, home) {
		return filepath.Join(home, id), nil
	}
	dir := nextToProject(root)
	if parent := filepath.Dir(root); parent != root && isSameVolume(root, parent) {
		err := os.Mkdir(dir, 0o700)
		if err == nil || errors.Is(err, fs.ErrExist) {
			// Its name is predictable, so in a shared parent another user
			// could create it first.
			return dir, checkPrivateDir(dir)
		}
	}
	if isInside {
		return "", fmt.Errorf("%s: %w", root, ErrStoreInsideProject)
	}
	return filepath.Join(home, id), nil
}

// nextToProject returns where the store of the project at root lives when it
// cannot live under home.
func nextToProject(root string) string {
	return filepath.Join(filepath.Dir(root), ".undo-"+project.ID(root))
}

// checkPrivateDir returns ErrUnsafeStore unless dir is a directory, not a
// symlink, owned by the current user and writable by nobody else.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: %w", dir, ErrUnsafeStore)
	}
	return nil
}

func isWithin(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func isSameVolume(a, b string) bool {
	aInfo, errA := os.Stat(a)
	bInfo, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return true
	}
	aStat, okA := aInfo.Sys().(*syscall.Stat_t)
	bStat, okB := bInfo.Sys().(*syscall.Stat_t)
	return !okA || !okB || aStat.Dev == bStat.Dev
}

// Root returns the project directory this store protects.
func (s *Store) Root() string { return s.root }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// IsCopyMode reports whether the store keeps full copies instead of clones.
func (s *Store) IsCopyMode() bool { return s.config.IsCopyMode }

// SetCopyMode switches between clones and full copies, and saves the choice
// with the store.
func (s *Store) SetCopyMode(isEnabled bool) error {
	s.config.IsCopyMode = isEnabled
	return writeJSON(s.configPath(), s.config)
}

// clone copies src to dst the way the store keeps files: as a clone, or as a
// full copy in copy mode.
func (s *Store) clone(src, dst string) error {
	if s.config.IsCopyMode {
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

// StoresUnder returns the stores of projects whose folders are directly
// inside dir, including projects that were deleted, so they can still be
// found and restored. It looks under home and next to the projects in dir.
func StoresUnder(home, dir string) ([]*Store, error) {
	var candidates []string
	for _, parent := range []string{home, dir} {
		entries, err := os.ReadDir(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() && (parent == home || strings.HasPrefix(entry.Name(), ".undo-")) {
				candidates = append(candidates, filepath.Join(parent, entry.Name()))
			}
		}
	}
	var stores []*Store
	for _, storeDir := range candidates {
		s := &Store{dir: storeDir}
		if s.loadConfig() != nil || s.config.Root == "" || filepath.Dir(s.config.Root) != dir || checkPrivateDir(storeDir) != nil {
			continue
		}
		s.root = s.config.Root
		stores = append(stores, s)
	}
	return stores, nil
}
