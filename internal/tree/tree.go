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

// Scan lists every path under root that matcher does not exclude. Symlinks
// are recorded but never followed. Directories it cannot read are left out.
func Scan(root string, matcher *exclude.Matcher) (Listing, error) {
	listing, _, err := ScanWithSkipped(root, matcher)
	return listing, err
}

// ScanWithSkipped works like Scan and also returns the paths it skipped. It
// does not go into excluded or unreadable directories.
func ScanWithSkipped(root string, matcher *exclude.Matcher) (Listing, Skipped, error) {
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
	var mu sync.Mutex
	var skipped Skipped
	err = walk.Dirs(directory{path: root, rel: "."}, func(dir directory) ([]directory, error) {
		children, err := os.ReadDir(dir.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			mu.Lock()
			skipped.Unreadable = append(skipped.Unreadable, dir.rel)
			mu.Unlock()
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
			if matcher.Match(rel, child.IsDir()) {
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
				mu.Lock()
				skipped.Unreadable = append(skipped.Unreadable, dir.rel)
				mu.Unlock()
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
		mu.Lock()
		maps.Copy(listing, found)
		skipped.Excluded = append(skipped.Excluded, excluded...)
		mu.Unlock()
		return subdirs, nil
	})
	if err != nil {
		return nil, Skipped{}, err
	}
	slices.Sort(skipped.Excluded)
	slices.Sort(skipped.Unreadable)
	return listing, skipped, nil
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
