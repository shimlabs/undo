package store

import (
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

// Source is the state of a path at a checkpoint and where its content is
// stored: in the base when Layer is 0, or in that checkpoint's layer.
type Source struct {
	Entry tree.Entry
	Layer int
	// IsLost is set when the stored copy was gone when its checkpoint was
	// merged into the base. A restore leaves such a path alone.
	IsLost bool
}

// ErrMergePending means an earlier merge of old checkpoints did not finish,
// so the stored states are not consistent yet. The next checkpoint retries
// the merge, and the error log says why it failed.
var ErrMergePending = errors.New("an earlier cleanup of old checkpoints has not finished")

// State returns every path of the project as it was at checkpoint id. It
// starts from the base and applies each layer up to id.
func (s *Store) State(id int) (map[string]Source, error) {
	if _, err := s.lstat(s.mergePath()); err == nil {
		return nil, ErrMergePending
	}
	if _, err := s.Checkpoint(id); err != nil {
		return nil, err
	}
	checkpoints, err := s.List()
	if err != nil {
		return nil, err
	}
	base, err := s.baseListing()
	if err != nil {
		return nil, err
	}
	record, err := s.readLost()
	if err != nil {
		return nil, err
	}
	lost := setOf(record.Paths)
	state := make(map[string]Source, len(base))
	for rel, entry := range base {
		state[rel] = Source{Entry: entry, IsLost: lost[rel]}
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > id {
			break
		}
		manifest, err := s.Manifest(checkpoint.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(state, rel)
		}
		for rel, entry := range manifest.Changed {
			state[rel] = Source{Entry: entry, Layer: checkpoint.ID}
		}
	}
	return state, nil
}

// Manifest returns the manifest of checkpoint id.
func (s *Store) Manifest(id int) (Manifest, error) {
	var manifest Manifest
	if err := s.readJSON(s.manifestPath(id), &manifest); err != nil {
		return Manifest{}, err
	}
	paths := slices.Concat(manifest.Deleted, manifest.Unstable, manifest.Unreadable, slices.Collect(maps.Keys(manifest.Changed)))
	return manifest, checkLocal(slices.Values(paths))
}

// ErrPathOutsideProject means a stored path points outside the project.
// Undo never writes such a path, so the store was changed by someone else.
var ErrPathOutsideProject = errors.New("a stored path points outside the project")

// checkLocal rejects stored paths that are absolute or climb out of the
// project with "..", so restoring or merging cannot touch other files.
func checkLocal(paths iter.Seq[string]) error {
	for rel := range paths {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return fmt.Errorf("%q: %w", rel, ErrPathOutsideProject)
		}
	}
	return nil
}

// ExtractAt writes the stored copy of the file or symlink rel, as recorded
// in source, to name in dir. An existing name is replaced in one rename, so
// it is never left half written. It works relative to dir and reads the
// copy through the store's root, so no symlink on either side is followed.
func (s *Store) ExtractAt(rel string, source Source, dir *os.File, name string) error {
	srcDir, srcName, err := s.openStored(rel, source)
	if err != nil {
		return err
	}
	defer srcDir.Close()
	dirFd := int(dir.Fd())
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return s.cloneAt(srcDir, srcName, dir, name)
	}
	tempName := atomicfile.TempName(name)
	if err := s.cloneAt(srcDir, srcName, dir, tempName); err != nil {
		return err
	}
	if err := unix.Renameat(dirFd, tempName, dirFd, name); err != nil {
		return errors.Join(&os.LinkError{Op: "renameat", Old: tempName, New: name, Err: err}, unix.Unlinkat(dirFd, tempName, 0))
	}
	return nil
}

// openStored opens the directory that holds the copy of rel recorded in
// source, and returns it with the copy's name. It returns an error wrapping
// ErrStoredFileMissing if the copy is gone.
func (s *Store) openStored(rel string, source Source) (*os.File, string, error) {
	src := s.basePath(rel)
	if source.Layer > 0 {
		src = s.layerFile(source.Layer, rel)
	}
	if _, err := s.lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("%s: %w", rel, ErrStoredFileMissing)
	}
	dir, err := s.openDir(filepath.Dir(src))
	if err != nil {
		return nil, "", err
	}
	return dir, filepath.Base(src), nil
}

// ExtractBaseDir writes the whole directory rel from the base to name in
// dir, which must not exist, in one clone. Only use it for a directory
// UnchangedDirs returned.
func (s *Store) ExtractBaseDir(rel string, dir *os.File, name string) error {
	srcDir, srcName, err := s.openStored(rel, Source{})
	if err != nil {
		return err
	}
	defer srcDir.Close()
	return clone.FileAt(srcDir, srcName, dir, name)
}

// UnchangedDirs returns the directories among dirs whose whole content in
// state is exactly what the base holds: everything inside them comes from
// the base, nothing was deleted from them since, and none of them holds a
// named pipe or other special file, which the base may not keep. Such a
// directory can be written with one ExtractBaseDir.
// It returns none where a directory cannot be cloned in one call: on Linux and
// in copy mode.
func (s *Store) UnchangedDirs(state map[string]Source, dirs []string) (map[string]bool, error) {
	if !clone.CanCloneDirs || s.IsCopyMode() {
		return nil, nil
	}
	candidates := map[string]bool{}
	for _, rel := range dirs {
		candidates[rel] = true
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	base, err := s.baseListing()
	if err != nil {
		return nil, err
	}
	inState, inBase := map[string]int{}, map[string]int{}
	for rel, source := range state {
		for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
			if candidates[dir] {
				inState[dir]++
				if source.Layer != 0 || source.Entry.Kind == tree.Other || source.IsLost {
					candidates[dir] = false
				}
			}
		}
	}
	for rel := range base {
		for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
			if candidates[dir] {
				inBase[dir]++
			}
		}
	}
	unchanged := map[string]bool{}
	for dir, isCandidate := range candidates {
		if isCandidate && base[dir].Kind == tree.Dir && inState[dir] == inBase[dir] {
			unchanged[dir] = true
		}
	}
	return unchanged, nil
}
