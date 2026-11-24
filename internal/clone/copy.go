//go:build darwin || linux

package clone

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/shimlabs/undo/internal/appledouble"
	"golang.org/x/sys/unix"
)

// CopyAt copies srcName in srcDir to name in dir, including a whole
// directory, keeping modes and modification times. It is for filesystems
// that cannot clone. It works relative to both directories and never follows
// a symlink, so a symlink placed in either tree cannot lead it elsewhere. It
// fails if name already exists, unless name starts with "._" on a filesystem
// where macOS keeps extended attributes in such files; that name is removed
// and created again.
func CopyAt(srcDir *os.File, srcName string, dir *os.File, name string) error {
	return copyTreeAt(srcDir, srcName, dir, name, copyOptions{copyContent: copyBytes, shouldCopyAttributes: true})
}

// copyOptions says how copyTreeAt fills what it creates.
type copyOptions struct {
	// copyContent fills the regular file out with the bytes of in.
	copyContent func(in, out *os.File) error
	// shouldCopyAttributes says whether files and directories get the
	// source's extended attributes too, as clones on macOS do.
	shouldCopyAttributes bool
}

// copyBytes copies the bytes of in to out.
func copyBytes(in, out *os.File) error {
	_, err := io.Copy(out, in)
	return err
}

// copyTreeAt recreates srcName in srcDir as name in dir and fills each
// regular file as options say. Sockets and devices are skipped.
func copyTreeAt(srcDir *os.File, srcName string, dir *os.File, name string, options copyOptions) error {
	srcFd, dirFd := int(srcDir.Fd()), int(dir.Fd())
	var stat unix.Stat_t
	if err := unix.Fstatat(srcFd, srcName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "fstatat", Path: srcName, Err: err}
	}
	perm := toUint32(stat.Mode) & 0o7777
	var err error
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		err = copyFileAt(srcFd, srcName, dirFd, name, perm, options)
	case unix.S_IFDIR:
		err = copyDirAt(srcFd, srcName, dir, name, perm, options)
	case unix.S_IFLNK:
		err = copySymlinkAt(srcFd, srcName, dirFd, name)
	case unix.S_IFIFO:
		err = MkfifoAt(dir, name, fs.FileMode(perm))
	default:
		return nil
	}
	if err != nil {
		return err
	}
	modTime := stat.Mtim
	if err := unix.UtimesNanoAt(dirFd, name, []unix.Timespec{modTime, modTime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "utimensat", Path: name, Err: err}
	}
	return nil
}

func copyFileAt(srcFd int, srcName string, dirFd int, name string, perm uint32, options copyOptions) error {
	// A file swapped for a named pipe since it was listed would block a
	// plain open, and is no longer the file to copy.
	inFd, err := unix.Openat(srcFd, srcName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return openatError(srcName, err)
	}
	in := os.NewFile(uintptr(inFd), srcName)
	defer in.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(inFd, &stat); err != nil {
		return &os.PathError{Op: "fstat", Path: srcName, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return &os.PathError{Op: "copy", Path: srcName, Err: unix.ENOENT}
	}
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
	outFd, err := unix.Openat(dirFd, name, flags, 0o600)
	if errors.Is(err, unix.EEXIST) && strings.HasPrefix(name, appledouble.Prefix) && appledouble.IsUsedAt(dirFd) {
		// macOS made this "._" file when the file it belongs to was copied.
		// Removing the name, rather than opening it, never writes through
		// whatever stands there.
		if err := unix.Unlinkat(dirFd, name, 0); err != nil {
			return &os.PathError{Op: "unlinkat", Path: name, Err: err}
		}
		outFd, err = unix.Openat(dirFd, name, flags, 0o600)
	}
	if err != nil {
		return openatError(name, err)
	}
	out := os.NewFile(uintptr(outFd), name)
	err = options.copyContent(in, out)
	if err == nil && options.shouldCopyAttributes {
		err = copyExtendedAttributes(in, out)
	}
	if err == nil {
		if chmodErr := unix.Fchmod(outFd, perm); chmodErr != nil {
			err = &os.PathError{Op: "fchmod", Path: name, Err: chmodErr}
		}
	}
	err = errors.Join(err, out.Close())
	if err != nil {
		err = errors.Join(err, unix.Unlinkat(dirFd, name, 0))
	}
	return err
}

func copyDirAt(srcFd int, srcName string, dir *os.File, name string, perm uint32, options copyOptions) error {
	dirFd := int(dir.Fd())
	if err := unix.Mkdirat(dirFd, name, 0o700); err != nil {
		return &os.PathError{Op: "mkdirat", Path: name, Err: err}
	}
	src, err := openDirAt(srcFd, srcName)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := openDirAt(dirFd, name)
	if err != nil {
		return err
	}
	defer dst.Close()
	if options.shouldCopyAttributes {
		if err := copyExtendedAttributes(src, dst); err != nil {
			return err
		}
	}
	entries, err := src.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		err := copyTreeAt(src, entry.Name(), dst, entry.Name(), options)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return JoinErrorPath(name, err)
		}
	}
	if err := unix.Fchmod(int(dst.Fd()), perm); err != nil {
		return &os.PathError{Op: "fchmod", Path: name, Err: err}
	}
	return nil
}

func copySymlinkAt(srcFd int, srcName string, dirFd int, name string) error {
	buffer := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(srcFd, srcName, buffer)
	if err != nil {
		return &os.PathError{Op: "readlinkat", Path: srcName, Err: err}
	}
	if err := unix.Symlinkat(string(buffer[:n]), dirFd, name); err != nil {
		return &os.PathError{Op: "symlinkat", Path: name, Err: err}
	}
	return nil
}

// openDirAt opens the directory name in dirFd without following a symlink.
func openDirAt(dirFd int, name string) (*os.File, error) {
	fd, err := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openatError(name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// toUint32 converts a file mode, whose type differs between platforms.
func toUint32[T ~uint16 | ~uint32](mode T) uint32 {
	return uint32(mode)
}

// PermissionBits returns the part of mode that chmod sets.
func PermissionBits(mode fs.FileMode) fs.FileMode {
	return mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}

// JoinErrorPath returns err with the path it names, when that is relative,
// taken as inside dir, so an error from deep in a copy says which file
// failed.
func JoinErrorPath(dir string, err error) error {
	if pathErr, ok := errors.AsType[*os.PathError](err); ok && !path.IsAbs(pathErr.Path) {
		pathErr.Path = path.Join(dir, pathErr.Path)
	}
	return err
}

func openatError(name string, err error) error {
	return &os.PathError{Op: "openat", Path: name, Err: err}
}
