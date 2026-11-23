package tree

import "path/filepath"

// StoredName returns the last element of path. Linux filesystems compare
// names byte for byte, so a path only reaches a file under its own name.
func StoredName(path string) (string, error) {
	return filepath.Base(path), nil
}
