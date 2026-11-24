//go:build darwin || linux

package clone

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// CopyExtendedAttributes gives dst, a file or directory, the extended
// attributes of src. An attribute the destination refuses, as a filesystem
// without extended attributes or a protected system attribute does, is left
// out, and so is one removed from src while it is copied.
func CopyExtendedAttributes(src, dst *os.File) error {
	srcFd, dstFd := int(src.Fd()), int(dst.Fd())
	names, err := readAttributeNames(srcFd)
	if err != nil {
		return &os.PathError{Op: "flistxattr", Path: src.Name(), Err: err}
	}
	for _, attr := range names {
		value, err := readAttribute(srcFd, attr)
		if errors.Is(err, errAttributeGone) {
			continue
		}
		if err != nil {
			return &os.PathError{Op: "fgetxattr", Path: src.Name(), Err: err}
		}
		err = unix.Fsetxattr(dstFd, attr, value, 0)
		if err != nil && !isRefusedAttribute(err) {
			return &os.PathError{Op: "fsetxattr", Path: dst.Name(), Err: err}
		}
	}
	return nil
}

// readAttributeNames returns the names of the extended attributes of the
// file open as fd. A filesystem without extended attributes has none.
func readAttributeNames(fd int) ([]string, error) {
	size, err := unix.Flistxattr(fd, nil)
	if errors.Is(err, unix.ENOTSUP) || size == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := make([]byte, size)
	if size, err = unix.Flistxattr(fd, list); err != nil {
		return nil, err
	}
	return strings.FieldsFunc(string(list[:size]), func(r rune) bool { return r == 0 }), nil
}

func readAttribute(fd int, attr string) ([]byte, error) {
	size, err := unix.Fgetxattr(fd, attr, nil)
	if err != nil {
		return nil, err
	}
	value := make([]byte, size)
	size, err = unix.Fgetxattr(fd, attr, value)
	return value[:max(size, 0)], err
}

func isRefusedAttribute(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}
