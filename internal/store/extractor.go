package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/clone"
	"golang.org/x/sys/unix"
)

// Extractor writes stored copies out of the store. It keeps the store
// directories it reads from open, so a restore or merge of many files walks
// the store root to each directory once. It is safe for concurrent use, and
// must be closed.
type Extractor struct {
	store      *Store
	storedDirs *dirCache
}

// NewExtractor returns an Extractor for the store. The store must stay locked
// while it is in use, so no stored directory it holds open is removed.
func (s *Store) NewExtractor() *Extractor {
	return &Extractor{store: s, storedDirs: s.newDirCache(nil)}
}

// Close closes the directories the Extractor holds open.
func (e *Extractor) Close() error {
	return e.storedDirs.reset()
}

// ExtractAt writes the stored copy of the file or symlink rel, as recorded
// in source, to name in dir. An existing name is replaced in one rename, so
// it is never left half written. It works relative to dir and reads the
// copy through the store's root, so no symlink on either side is followed.
// It returns an error wrapping ErrStoredFileMissing if the copy is gone.
func (e *Extractor) ExtractAt(rel string, source Source, dir *os.File, name string) error {
	srcDir, srcName, release, err := e.openStored(rel, source)
	if err != nil {
		return err
	}
	defer release()
	dirFd := int(dir.Fd())
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return e.store.cloneAt(srcDir, srcName, dir, name)
	}
	tempName := atomicfile.TempName(name)
	if err := e.store.cloneAt(srcDir, srcName, dir, tempName); err != nil {
		return err
	}
	if err := unix.Renameat(dirFd, tempName, dirFd, name); err != nil {
		return errors.Join(&os.LinkError{Op: "renameat", Old: tempName, New: name, Err: err}, unix.Unlinkat(dirFd, tempName, 0))
	}
	return nil
}

// HasContent reports whether file holds the same bytes as the stored copy of
// the file rel recorded in source.
func (e *Extractor) HasContent(rel string, source Source, file *os.File) (bool, error) {
	srcDir, srcName, release, err := e.openStored(rel, source)
	if err != nil {
		return false, err
	}
	defer release()
	fd, err := unix.Openat(int(srcDir.Fd()), srcName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, &os.PathError{Op: "openat", Path: srcName, Err: err}
	}
	stored := os.NewFile(uintptr(fd), srcName)
	defer stored.Close()
	return sameBytes(stored, file)
}

// contentChunkSize is how much of each file sameBytes compares at a time.
const contentChunkSize = 64 << 10

// sameBytes reports whether first and second hold the same bytes.
func sameBytes(first, second io.Reader) (bool, error) {
	firstChunk, secondChunk := make([]byte, contentChunkSize), make([]byte, contentChunkSize)
	for {
		firstCount, firstErr := io.ReadFull(first, firstChunk)
		secondCount, secondErr := io.ReadFull(second, secondChunk)
		if !bytes.Equal(firstChunk[:firstCount], secondChunk[:secondCount]) {
			return false, nil
		}
		isFirstDone := errors.Is(firstErr, io.EOF) || errors.Is(firstErr, io.ErrUnexpectedEOF)
		isSecondDone := errors.Is(secondErr, io.EOF) || errors.Is(secondErr, io.ErrUnexpectedEOF)
		switch {
		case firstErr != nil && !isFirstDone:
			return false, firstErr
		case secondErr != nil && !isSecondDone:
			return false, secondErr
		case isFirstDone || isSecondDone:
			return isFirstDone && isSecondDone, nil
		}
	}
}

// ExtractBaseDir writes the whole directory rel from the base to name in
// dir, which must not exist, in one clone. Only use it for a directory
// UnchangedDirs returned.
func (e *Extractor) ExtractBaseDir(rel string, dir *os.File, name string) error {
	srcDir, srcName, release, err := e.openStored(rel, Source{})
	if err != nil {
		return err
	}
	defer release()
	return clone.FileAt(srcDir, srcName, dir, name)
}

// openStored returns the open directory that holds the copy of rel recorded
// in source, the copy's name in it, and a function to call once done with
// the directory.
func (e *Extractor) openStored(rel string, source Source) (*os.File, string, func(), error) {
	src := e.store.basePath(rel)
	if source.Layer > 0 {
		src = e.store.layerFile(source.Layer, rel)
	}
	srcDir, release, err := e.storedDirs.open(filepath.Dir(src))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil, fmt.Errorf("%s: %w", rel, ErrStoredFileMissing)
	}
	if err != nil {
		return nil, "", nil, err
	}
	srcName := filepath.Base(src)
	var stat unix.Stat_t
	err = unix.Fstatat(int(srcDir.Fd()), srcName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		release()
		return nil, "", nil, fmt.Errorf("%s: %w", rel, ErrStoredFileMissing)
	}
	if err != nil {
		release()
		return nil, "", nil, &os.PathError{Op: "fstatat", Path: srcName, Err: err}
	}
	return srcDir, srcName, release, nil
}
