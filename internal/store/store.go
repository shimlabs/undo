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
	"sync/atomic"
	"syscall"

	"github.com/shimlabs/undo/internal/atomicfile"
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
	// ErrStoredFileMissing means the stored copy of a file is gone. The
	// newest checkpoint's copies can be lost in a power cut, since they are
	// not flushed to disk.
	ErrStoredFileMissing = errors.New("the stored copy of this file is missing")
	// ErrStoreInsideProject means the project holds the store home, and no
	// folder next to it can hold the store instead.
	ErrStoreInsideProject = errors.New("this folder holds the undo store and has no writable parent on the same volume")
)

// Store is the data undo keeps on disk for one project. Several goroutines
// may use one Store, such as the watcher's server and its pruner.
type Store struct {
	root string
	dir  string
	// config is read again whenever the store is locked, so it is replaced
	// as a whole.
	config atomic.Pointer[config]
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
//
// A store next to the project is only used if home recorded creating it.
// Its name is predictable, so anyone who can write next to the project, or
// an archive unpacked there, could otherwise plant one.
func Open(home, root string) (*Store, error) {
	dirs := []string{filepath.Join(home, project.ID(root))}
	if isRecorded(home, nextToProject(root)) {
		dirs = append(dirs, nextToProject(root))
	}
	for _, dir := range dirs {
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

// isRecorded reports whether home recorded creating the store dir next to a
// project.
func isRecorded(home, dir string) bool {
	recorded, err := os.ReadFile(locationPath(home, dir))
	return err == nil && string(recorded) == dir
}

// recordLocation records in home that it created the store dir next to a
// project.
func recordLocation(home, dir string) error {
	return atomicfile.WriteFile(locationPath(home, dir), []byte(dir), 0o600)
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
	// Merges and restores work on paths below these, so a symlink in their
	// place would lead them outside the store.
	for _, name := range []string{baseDirName, layersDirName} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), ErrUnsafeStore)
		}
	}
	s := &Store{root: root, dir: dir}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	settings := *s.config.Load()
	if settings.Format > format {
		return nil, fmt.Errorf("%s: %w", dir, ErrNewerFormat)
	}
	if settings.Format == 0 {
		settings.Format, settings.Root = format, root
		if err := writeJSON(s.configPath(), settings); err != nil {
			return nil, err
		}
		s.config.Store(&settings)
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
		if errors.Is(err, fs.ErrExist) && !isRecorded(home, dir) {
			return "", fmt.Errorf("%s was not created by undo: %w", dir, ErrUnsafeStore)
		}
		if err == nil || errors.Is(err, fs.ErrExist) {
			if err := checkPrivateDir(dir); err != nil {
				return "", err
			}
			return dir, recordLocation(home, dir)
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
	return filepath.Join(filepath.Dir(root), storeDirPrefix+project.ID(root))
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
func (s *Store) IsCopyMode() bool { return s.config.Load().IsCopyMode }

// SetCopyMode switches between clones and full copies, and saves the choice
// with the store.
func (s *Store) SetCopyMode(isEnabled bool) error {
	settings := *s.config.Load()
	settings.IsCopyMode = isEnabled
	if err := writeJSON(s.configPath(), settings); err != nil {
		return err
	}
	s.config.Store(&settings)
	return nil
}

// clone copies src to dst the way the store keeps files: as a clone, or as a
// full copy in copy mode.
func (s *Store) clone(src, dst string) error {
	if s.IsCopyMode() {
		return clone.Copy(src, dst)
	}
	return clone.File(src, dst)
}

func (s *Store) cloneAt(src string, dir *os.File, name string) error {
	if s.IsCopyMode() {
		return clone.CopyAt(src, dir, name)
	}
	return clone.FileAt(src, dir, name)
}

// loadConfig reads the saved settings. It runs whenever the store is locked,
// so a long-running watcher sees copy mode turned on by another process.
func (s *Store) loadConfig() error {
	var settings config
	if err := readJSON(s.configPath(), &settings); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.config.Store(&settings)
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
			candidate := filepath.Join(parent, entry.Name())
			if entry.IsDir() && (parent == home || strings.HasPrefix(entry.Name(), storeDirPrefix) && isRecorded(home, candidate)) {
				candidates = append(candidates, candidate)
			}
		}
	}
	var stores []*Store
	for _, storeDir := range candidates {
		s := &Store{dir: storeDir}
		if s.loadConfig() != nil || s.config.Load().Root == "" || filepath.Dir(s.config.Load().Root) != dir || checkPrivateDir(storeDir) != nil {
			continue
		}
		s.root = s.config.Load().Root
		stores = append(stores, s)
	}
	return stores, nil
}
