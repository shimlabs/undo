//go:build !darwin

// Package clone makes copy-on-write copies of files and directories.
package clone

import "errors"

// File clones src to dst without copying the data.
func File(src, dst string) error {
	return errors.ErrUnsupported
}

// Tree clones the whole directory at src to dst.
func Tree(src, dst string) error {
	return errors.ErrUnsupported
}
