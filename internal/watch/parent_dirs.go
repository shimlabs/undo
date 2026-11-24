package watch

import (
	"os"
	"path"

	"github.com/shimlabs/undo/internal/tree"
)

// maxOpenParents bounds how many folders a batch keeps open at once.
const maxOpenParents = 64

// parentDirs keeps the folders a batch of events looks in open. Each is
// reached from the root one name at a time, so a folder an agent swaps for a
// symlink is gone rather than followed out of the project.
type parentDirs struct {
	root  string
	files map[string]*os.File
}

func newParentDirs(root string) *parentDirs {
	return &parentDirs{root: root, files: map[string]*os.File{}}
}

// open returns the folder rel below the root, opening it on first use. A
// symlink or file anywhere on the way means the folder is gone, and the
// error matches fs.ErrNotExist.
func (d *parentDirs) open(rel string) (*os.File, error) {
	if file, isOpen := d.files[rel]; isOpen {
		return file, nil
	}
	if len(d.files) >= maxOpenParents {
		d.close()
	}
	file, err := d.openUncached(rel)
	if err != nil {
		return nil, err
	}
	d.files[rel] = file
	return file, nil
}

func (d *parentDirs) close() {
	for _, file := range d.files {
		file.Close()
	}
	clear(d.files)
}

// openUncached opens rel in its parent, itself opened through the cache, so
// each folder on the way is opened once per batch rather than once per path.
// The file it returns is not cached, so its offset is the caller's own.
func (d *parentDirs) openUncached(rel string) (*os.File, error) {
	if rel == "." {
		return tree.OpenDir(d.root, ".")
	}
	parent, err := d.open(path.Dir(rel))
	if err != nil {
		return nil, err
	}
	return tree.OpenDirAt(parent, path.Base(rel))
}
