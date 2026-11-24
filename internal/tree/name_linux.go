package tree

import (
	"os"
	"path/filepath"
)

// StoredName returns the last element of path. Linux filesystems compare
// names byte for byte, so a path only reaches a file under its own name.
func StoredName(path string) (string, error) {
	return filepath.Base(path), nil
}

// StoredNameAt returns name, as StoredName does.
func StoredNameAt(_ *os.File, name string) (string, error) {
	return name, nil
}
