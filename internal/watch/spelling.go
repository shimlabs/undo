package watch

import (
	"maps"
	"path"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/tree"
)

// storedSpelling returns fullPath with each directory in it spelled as the
// filesystem stores it. On a filesystem that ignores case and Unicode form,
// an event spells a path as the command that changed it did. storedDirs
// keeps the directories already looked up, and each is looked up in its
// parent reached through parents.
func (t *tracker) storedSpelling(parents *parentDirs, storedDirs map[string]string, fullPath string) string {
	dir := filepath.Dir(fullPath)
	if dir == t.root || !t.isInRoot(dir) {
		return fullPath
	}
	stored, ok := storedDirs[dir]
	if !ok {
		stored = t.storedSpelling(parents, storedDirs, dir)
		if name, err := t.storedNameIn(parents, stored); err == nil {
			stored = filepath.Join(filepath.Dir(stored), name)
		}
		storedDirs[dir] = stored
	}
	return filepath.Join(stored, filepath.Base(fullPath))
}

// storedNameIn returns the name the parent of fullPath, a path in the
// project, stores for it.
func (t *tracker) storedNameIn(parents *parentDirs, fullPath string) (string, error) {
	rel, err := filepath.Rel(t.root, filepath.Dir(fullPath))
	if err != nil {
		return "", err
	}
	parent, err := parents.open(filepath.ToSlash(rel))
	if err != nil {
		return "", err
	}
	return tree.StoredNameAt(parent, filepath.Base(fullPath))
}

// removeStaleSpellings removes the listed paths in changes.uncertainDirs that
// their directory no longer holds under that exact name.
func (t *tracker) removeStaleSpellings(parents *parentDirs, changes *changeSet) {
	if len(changes.uncertainDirs) == 0 {
		return
	}
	namesInDir := make(map[string]map[string]bool, len(changes.uncertainDirs))
	for dir := range changes.uncertainDirs {
		// A directory that is gone or cannot be read is left alone: its own
		// event or the next full comparison updates what it held.
		if names, err := readNames(parents, dir); err == nil {
			namesInDir[dir] = names
		}
	}
	if t.children == nil {
		t.children = newChildIndex(t.listing)
	}
	// The index holds the listing as the last checkpoint left it, so the
	// paths this batch added are checked as well, grouped by folder in one
	// pass.
	addedInDir := map[string][]string{}
	for rel := range changes.changed {
		if dir := path.Dir(rel); rel != "." && namesInDir[dir] != nil {
			addedInDir[dir] = append(addedInDir[dir], rel)
		}
	}
	for dir, names := range namesInDir {
		listed := append(slices.Collect(maps.Keys(t.children[dir])), addedInDir[dir]...)
		for _, rel := range listed {
			if entry, isListed := changes.listing[rel]; isListed && !names[path.Base(rel)] {
				changes.removeTree(rel, entry.Kind == tree.Dir)
			}
		}
	}
}

// childIndex maps each listed folder to the paths listed directly in it, so
// finding them does not take a pass over the whole listing.
type childIndex map[string]map[string]bool

func newChildIndex(listing tree.Listing) childIndex {
	index := childIndex{}
	for rel := range listing {
		index.add(rel)
	}
	return index
}

func (index childIndex) add(rel string) {
	if rel == "." {
		return
	}
	dir := path.Dir(rel)
	if index[dir] == nil {
		index[dir] = map[string]bool{}
	}
	index[dir][rel] = true
}

// update applies the paths a checkpoint changed and deleted. A nil index,
// not built yet, stays nil.
func (index childIndex) update(changed, deleted []string) {
	if index == nil {
		return
	}
	for _, rel := range deleted {
		delete(index[path.Dir(rel)], rel)
		if len(index[path.Dir(rel)]) == 0 {
			delete(index, path.Dir(rel))
		}
	}
	for _, rel := range changed {
		index.add(rel)
	}
}

// readNames returns the names the directory dir holds, as it stores them.
// The directory is opened in its parent reached through parents, so a named
// pipe or symlink in its place fails at once instead of blocking or leading
// elsewhere.
func readNames(parents *parentDirs, dir string) (map[string]bool, error) {
	file, err := parents.openUncached(dir)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	list, err := file.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(list))
	for _, name := range list {
		names[name] = true
	}
	return names, nil
}
