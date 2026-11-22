//go:build !darwin && !linux

// Package clone makes copy-on-write copies of files and directories.
package clone

import "errors"

// File is not supported on this platform.
func File(src, dst string) error {
	return errors.ErrUnsupported
}

// Copy is not supported on this platform.
func Copy(src, dst string) error {
	return errors.ErrUnsupported
}
