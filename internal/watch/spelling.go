package watch

import (
	"path"
	"path/filepath"

	"github.com/shimlabs/undo/internal/tree"
)

// storedSpelling returns fullPath with each directory in it spelled as the
// filesystem stores it. On a filesystem that ignores case and Unicode form,
// an event spells a path as the command that changed it did. storedDirs
// keeps the directories already looked up.
func (t *tracker) storedSpelling(storedDirs map[string]string, fullPath string) string {
	dir := filepath.Dir(fullPath)
	if dir == t.root || !t.isInRoot(dir) {
		return fullPath
	}
	stored, ok := storedDirs[dir]
	if !ok {
		stored = t.storedSpelling(storedDirs, dir)
		if name, err := tree.StoredName(stored); err == nil {
			stored = filepath.Join(filepath.Dir(stored), name)
		}
		storedDirs[dir] = stored
	}
	return filepath.Join(stored, filepath.Base(fullPath))
}

// removeStaleSpellings removes the listed paths in changes.uncertainDirs that
// their directory no longer holds under that exact name.
func (t *tracker) removeStaleSpellings(changes *changeSet) {
	if len(changes.uncertainDirs) == 0 {
		return
	}
	namesInDir := make(map[string]map[string]bool, len(changes.uncertainDirs))
	for dir := range changes.uncertainDirs {
		// A directory that is gone or cannot be read is left alone: its own
		// event or the next full comparison updates what it held.
		if names, err := readNames(t.root, dir); err == nil {
			namesInDir[dir] = names
		}
	}
	for rel, entry := range changes.listing {
		if rel == "." {
			continue
		}
		if names, ok := namesInDir[path.Dir(rel)]; ok && !names[path.Base(rel)] {
			changes.removeTree(rel, entry.Kind == tree.Dir)
		}
	}
}

// readNames returns the names the directory dir below root holds, as it
// stores them. The directory is opened one name at a time, so a named pipe
// or symlink in its place fails at once instead of blocking or leading
// elsewhere.
func readNames(root, dir string) (map[string]bool, error) {
	file, err := tree.OpenDir(root, dir)
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
