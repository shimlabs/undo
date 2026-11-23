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
	"strings"
	"sync"
	"syscall"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/walk"
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
	Kind       Kind        `json:"kind"`
	Mode       fs.FileMode `json:"mode"`
	Size       int64       `json:"size"`
	ModTime    int64       `json:"mtime"`
	ChangeTime int64       `json:"ctime"`
	Inode      uint64      `json:"ino"`
	Link       string      `json:"link,omitempty"`
	// LinkCount is how many names a file has. A write through any of them
	// changes all of them.
	LinkCount uint64 `json:"link_count,omitempty"`
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
	entry, err := Stat(root)
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
	scan := scanner{matcher: matcher, listing: listing}
	if err := walk.Dirs(directory{path: root, rel: "."}, scan.readDir); err != nil {
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
// is recorded as unreadable instead.
func (s *scanner) readDir(dir directory) ([]directory, error) {
	children, err := os.ReadDir(dir.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, fs.ErrPermission) {
		s.addUnreadable(dir.rel)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found := make(Listing, len(children))
	var excluded []string
	var subdirs []directory
	for _, child := range children {
		rel := child.Name()
		if dir.rel != "." {
			rel = dir.rel + "/" + child.Name()
		}
		if s.matcher.Match(rel, child.IsDir()) {
			excluded = append(excluded, rel)
			continue
		}
		path := filepath.Join(dir.path, child.Name())
		entry, err := Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, fs.ErrPermission) {
			// The directory can be listed but not entered.
			s.addUnreadable(dir.rel)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		found[rel] = entry
		if entry.Kind == Dir {
			subdirs = append(subdirs, directory{path: path, rel: rel})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.listing, found)
	s.skipped.Excluded = append(s.skipped.Excluded, excluded...)
	return subdirs, nil
}

func (s *scanner) addUnreadable(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped.Unreadable = append(s.skipped.Unreadable, rel)
}

type directory struct {
	path string
	rel  string
}

// Stat describes the file at path. If it is a symlink, the link itself is
// described, not its target.
func Stat(path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Entry{}, &fs.PathError{Op: "stat", Path: path, Err: errors.ErrUnsupported}
	}
	modTime, changeTime := statTimes(stat)
	entry := Entry{
		Mode:       info.Mode(),
		Size:       info.Size(),
		ModTime:    modTime,
		ChangeTime: changeTime,
		Inode:      stat.Ino,
	}
	switch {
	case info.Mode().IsRegular():
		entry.Kind = File
		entry.LinkCount = toUint64(stat.Nlink)
	case info.IsDir():
		entry.Kind = Dir
		entry.Size = 0
	case info.Mode()&fs.ModeSymlink != 0:
		entry.Kind = Symlink
		if entry.Link, err = os.Readlink(path); err != nil {
			return Entry{}, err
		}
	default:
		entry.Kind = Other
	}
	return entry, nil
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

// Depth returns how many directories deep rel is below the root, with -1 for
// the root itself, so sorting by depth puts parents before their children.
func Depth(rel string) int {
	if rel == "." {
		return -1
	}
	return strings.Count(rel, "/")
}

// toUint64 converts a link count, whose type differs between platforms.
func toUint64[T ~uint16 | ~uint32 | ~uint64](n T) uint64 {
	return uint64(n)
}
