package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

// Extractor writes stored copies out of the store. It keeps the store
// directories it reads from open, so a restore or merge of many files walks
// the store root to each directory once. It is safe for concurrent use, and
// must be closed.
type Extractor struct {
	store      *Store
	storedDirs *dirCache
	// chunkPool holds the buffer pairs HasContent compares files in, so
	// comparing many small files does not allocate two chunks for each.
	chunkPool sync.Pool
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
	stored, err := tree.OpenFileAt(srcDir, srcName)
	if err != nil {
		return false, err
	}
	defer stored.Close()
	chunks, isPooled := e.chunkPool.Get().(*[2][]byte)
	if !isPooled {
		chunks = &[2][]byte{make([]byte, contentChunkSize), make([]byte, contentChunkSize)}
	}
	defer e.chunkPool.Put(chunks)
	return sameBytes(stored, file, chunks[0], chunks[1])
}

// contentChunkSize is how much of each file sameBytes compares at a time.
const contentChunkSize = 64 << 10

// sameBytes reports whether first and second hold the same bytes, reading
// them into firstChunk and secondChunk, which must be the same size.
func sameBytes(first, second io.Reader, firstChunk, secondChunk []byte) (bool, error) {
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

// CopyDirAttributes gives dir, the directory rel was restored as, the
// extended attributes stored with rel as source records. A directory a
// checkpoint stored no copy of keeps the attributes it has.
func (e *Extractor) CopyDirAttributes(rel string, source Source, dir *os.File) error {
	srcDir, srcName, release, err := e.openStored(rel, source)
	if errors.Is(err, ErrStoredFileMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	stored, err := tree.OpenDirAt(srcDir, srcName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer stored.Close()
	return clone.CopyExtendedAttributes(stored, dir)
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
		// The store's root names paths inside the store, which callers would
		// take for paths in the project.
		return nil, "", nil, clone.JoinErrorPath(e.store.dir, err)
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
