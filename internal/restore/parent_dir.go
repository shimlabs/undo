package restore

import (
	"os"
	"path"
	"path/filepath"
)

// parentDir keeps open the directory that holds the path being written or
// removed. Paths are sorted by depth and then by name, so the paths of one
// directory come one after another and share one open, instead of each
// walking down from the project root.
type parentDir struct {
	root *os.Root
	rel  string
	file *os.File
}

// open returns the directory that holds rel. The first time it opens a
// directory, it gives the user write and search permission on it, so the
// restore can change a read-only directory; the directory's own mode is set
// back at the end.
func (d *parentDir) open(rel string) (*os.File, error) {
	dir := path.Dir(rel)
	if d.file != nil && d.rel == dir {
		return d.file, nil
	}
	d.close()
	file, err := d.root.Open(filepath.FromSlash(dir))
	if err != nil {
		return nil, err
	}
	d.rel, d.file = dir, file
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o300 != 0o300 {
		return file, file.Chmod(perm | 0o300)
	}
	return file, nil
}

func (d *parentDir) close() {
	if d.file != nil {
		_ = d.file.Close()
		d.rel, d.file = "", nil
	}
}
