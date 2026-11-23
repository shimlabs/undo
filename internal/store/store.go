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
	// ErrNotProject means the folder is a home directory or the root of the
	// disk. Taking checkpoints of it would copy far more than a project.
	ErrNotProject = errors.New("this is a home directory or the disk root, not a project folder")
	// ErrNewerFormat means a newer version of undo wrote the store.
	ErrNewerFormat = errors.New("this store was written by a newer version of undo")
	// ErrUnsafeStore means a store directory exists that is not a private
	// directory of the current user, so someone else could change it.
	ErrUnsafeStore = errors.New("the store directory must be a directory owned by you and private to you")
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
	IsCopyMode bool   `json:"copy"`
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
	dir, err := Locate(root)
	if err != nil {
		return nil, err
	}
	return OpenAt(dir, root)
}

// OpenAt returns the store in dir for the project at root and creates it if
// needed.
func OpenAt(dir, root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "layers"), 0o700); err != nil {
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

// Locate returns the directory of the store for the project at root. Stores
// live under Home. Clones only work within one volume, so when the project
// is on another volume, or when Home is inside the project, the store goes
// next to the project instead, as <parent>/.undo-<id>.
func Locate(root string) (string, error) {
	userHome, _ := os.UserHomeDir()
	if resolved, err := filepath.EvalSymlinks(userHome); err == nil {
		userHome = resolved
	}
	if root == "/" || root == userHome {
		return "", fmt.Errorf("%s: %w", root, ErrNotProject)
	}
	home, err := Home()
	if err != nil {
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
	inHome := filepath.Join(home, id)
	isInside := isWithin(filepath.Join(resolvedHome, id), root)
	if !isInside && isSameVolume(root, home) {
		return inHome, nil
	}
	parent := filepath.Dir(root)
	nextToProject := filepath.Join(parent, ".undo-"+id)
	if parent != root && isSameVolume(root, parent) {
		err := os.Mkdir(nextToProject, 0o700)
		if err == nil || errors.Is(err, fs.ErrExist) {
			// Its name is predictable, so in a shared parent another user
			// could create it first.
			return nextToProject, checkPrivateDir(nextToProject)
		}
	}
	if isInside {
		return "", fmt.Errorf("%s holds the undo store and has no writable parent on the same volume: %w", root, ErrNotProject)
	}
	return inHome, nil
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

func (s *Store) configPath() string {
	return filepath.Join(s.dir, "config.json")
}

// Stores returns every store under Home with the project it protects, so a
// project whose folder was deleted can still be found and restored.
func Stores() ([]*Store, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stores []*Store
	for _, entry := range entries {
		s := &Store{dir: filepath.Join(home, entry.Name())}
		if !entry.IsDir() || s.loadConfig() != nil || s.config.Root == "" {
			continue
		}
		s.root = s.config.Root
		stores = append(stores, s)
	}
	return stores, nil
}
