//go:build !darwin && !linux

package store

import "os"

// privateSize returns the size of path. Without a way to see shared blocks,
// this counts the whole file.
func privateSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
