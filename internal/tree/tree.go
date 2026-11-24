// Package tree lists the files in a project and finds what changed between
// two listings.
package tree

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/walk"
	"golang.org/x/sys/unix"
)

// Kind is the type of a filesystem entry.
type Kind uint8

// The kinds of entry a listing records. Other covers named pipes, sockets
// and devices.
const (
	File Kind = iota + 1
	Dir
	Symlink
	Other
)

// Entry describes a path at the time it was listed. Stores keep entries on
// disk. Mode holds fs.FileMode bits, which Go's compatibility promise keeps
// stable, so stored entries stay readable by later versions.
type Entry struct {
	Kind       Kind
	Mode       fs.FileMode
	Size       int64
	ModTime    int64
	ChangeTime int64
	Inode      uint64
	Link       string
	// LinkCount is how many names a file, named pipe or symlink has, and 0
	// when it has one. A write through any of them changes all of them.
	LinkCount uint64
}

// Listing maps paths to their entries. Paths are slash-separated and relative
// to the project root, and the root itself is ".".
type Listing map[string]Entry

// Skipped lists the paths a scan did not go into.
type Skipped struct {
	// Excluded are the paths the matcher excludes.
	Excluded []string
	// Unreadable are the directories undo has no permission to list, such as
	// a database folder owned by another user. Their own entries are in the
	// listing, but nothing inside them is.
	Unreadable []string
}

// Scan lists every path under root that matcher does not exclude,
// and returns the paths it skipped. Symlinks are recorded but never
// followed, and excluded or unreadable directories are not gone into.
func Scan(root string, matcher *exclude.Matcher) (Listing, Skipped, error) {
	entry, id, err := statAt(unix.AT_FDCWD, root)
	if errors.Is(err, fs.ErrNotExist) {
		return Listing{}, Skipped{}, nil
	}
	if err != nil {
		return nil, Skipped{}, err
	}
	listing := Listing{".": entry}
	if entry.Kind != Dir {
		return listing, Skipped{}, nil
	}
	return scanFrom(directory{path: root, rel: ".", id: id}, matcher, listing)
}

// ScanDir lists every path inside the directory rel below root, as Scan
// does, with paths relative to root. rel itself is left out. The directory
// is reached one name at a time, so a symlink anywhere on the way to it
// means it is gone.
func ScanDir(root, rel string, matcher *exclude.Matcher) (Listing, Skipped, error) {
	file, err := OpenDir(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return Listing{}, Skipped{}, nil
	}
	if err != nil {
		return nil, Skipped{}, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(int(file.Fd()), &stat)
	file.Close()
	if err != nil {
		return nil, Skipped{}, &fs.PathError{Op: "fstat", Path: file.Name(), Err: err}
	}
	dir := directory{path: filepath.Join(root, filepath.FromSlash(rel)), rel: rel, id: toFileID(&stat)}
	return scanFrom(dir, matcher, Listing{})
}

// scanFrom adds everything under dir to listing.
func scanFrom(dir directory, matcher *exclude.Matcher, listing Listing) (Listing, Skipped, error) {
	scan := scanner{matcher: matcher, listing: listing}
	if err := walk.Dirs(dir, scan.readDir); err != nil {
		return nil, Skipped{}, err
	}
	slices.Sort(scan.skipped.Excluded)
	slices.Sort(scan.skipped.Unreadable)
	return scan.listing, scan.skipped, nil
}

// scanner collects a listing from directories read at the same time.
type scanner struct {
	matcher *exclude.Matcher
	mu      sync.Mutex
	listing Listing
	skipped Skipped
}

// readDir adds dir's children to the listing and returns its subdirectories.
// A directory that cannot be listed, or whose children cannot be looked at,
// is recorded as unreadable instead. Each directory is opened by path, so it
// must be the one its parent's entry describes: a directory swapped for a
// symlink, or for another directory, since then counts as gone.
func (s *scanner) readDir(dir directory) ([]directory, error) {
	file, err := openListed(dir)
	switch {
	case errors.Is(err, errDirGone):
		return nil, nil
	case errors.Is(err, fs.ErrPermission):
		s.addUnreadable(dir.rel)
		return nil, nil
	case err != nil:
		return nil, err
	}
	defer file.Close()
	found, err := s.readEntries(dir, file)
	if errors.Is(err, fs.ErrPermission) {
		// The directory can be listed but not entered.
		s.addUnreadable(dir.rel)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.listing, found.listing)
	s.skipped.Excluded = append(s.skipped.Excluded, found.excluded...)
	return found.subdirs, nil
}

// dirEntries is what reading one directory found.
type dirEntries struct {
	listing  Listing
	excluded []string
	subdirs  []directory
}

// readEntries describes the children of dir, open as file, through the open
// directory.
func (s *scanner) readEntries(dir directory, file *os.File) (dirEntries, error) {
	fd := int(file.Fd())
	children, err := file.ReadDir(-1)
	if err != nil {
		return dirEntries{}, err
	}
	found := dirEntries{listing: make(Listing, len(children))}
	for _, child := range children {
		rel := child.Name()
		if dir.rel != "." {
			rel = dir.rel + "/" + child.Name()
		}
		if s.matcher.Match(rel, child.IsDir()) {
			found.excluded = append(found.excluded, rel)
			continue
		}
		entry, id, err := statAt(fd, child.Name())
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return dirEntries{}, err
		}
		found.listing[rel] = entry
		if entry.Kind == Dir {
			found.subdirs = append(found.subdirs, directory{path: filepath.Join(dir.path, child.Name()), rel: rel, id: id})
		}
	}
	return found, nil
}

// errDirGone reports that a directory is gone, or is no longer the one its
// parent listed.
var errDirGone = errors.New("directory is gone")

// openListed opens dir without following a symlink, and checks that it is
// the directory its parent listed.
func openListed(dir directory) (*os.File, error) {
	fd, err := unix.Open(dir.path, openDirFlags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, errDirGone
	}
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir.path, Err: err}
	}
	file := os.NewFile(uintptr(fd), dir.path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(&fs.PathError{Op: "fstat", Path: dir.path, Err: err}, file.Close())
	}
	if toFileID(&stat) != dir.id {
		return nil, errors.Join(errDirGone, file.Close())
	}
	return file, nil
}

func (s *scanner) addUnreadable(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped.Unreadable = append(s.skipped.Unreadable, rel)
}

type directory struct {
	path string
	rel  string
	id   fileID
}

// Changed returns the paths in current that are new or different from old,
// and the paths in old that are missing from current. Both lists are sorted.
func Changed(old, current Listing) (changed, deleted []string) {
	for path, entry := range current {
		if previous, ok := old[path]; !ok || previous != entry {
			changed = append(changed, path)
		}
	}
	for path := range old {
		if _, ok := current[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	slices.Sort(changed)
	slices.Sort(deleted)
	return changed, deleted
}
