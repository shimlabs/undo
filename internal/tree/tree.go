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

// Entry describes a path at the time it was listed.
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

// Scan lists every path under root that ex does not exclude. Symlinks are
// recorded but never followed.
func Scan(root string, ex *exclude.Matcher) (Listing, error) {
	listing, _, err := ScanWithExcluded(root, ex)
	return listing, err
}

// ScanWithExcluded works like Scan and also returns the excluded paths it
// skipped. It does not go into excluded directories.
func ScanWithExcluded(root string, ex *exclude.Matcher) (Listing, []string, error) {
	entry, err := Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return Listing{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	listing := Listing{".": entry}
	if entry.Kind != Dir {
		return listing, nil, nil
	}
	var mu sync.Mutex
	var excluded []string
	err = walk.Dirs(dir{path: root, rel: "."}, walk.Workers, func(d dir) ([]dir, error) {
		children, err := os.ReadDir(d.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		found := make(Listing, len(children))
		var skipped []string
		var subdirs []dir
		for _, child := range children {
			rel := child.Name()
			if d.rel != "." {
				rel = d.rel + "/" + child.Name()
			}
			if ex.Match(rel, child.IsDir()) {
				skipped = append(skipped, rel)
				continue
			}
			path := filepath.Join(d.path, child.Name())
			entry, err := Stat(path)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			found[rel] = entry
			if entry.Kind == Dir {
				subdirs = append(subdirs, dir{path: path, rel: rel})
			}
		}
		mu.Lock()
		maps.Copy(listing, found)
		excluded = append(excluded, skipped...)
		mu.Unlock()
		return subdirs, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return listing, excluded, nil
}

type dir struct {
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
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Entry{}, &fs.PathError{Op: "stat", Path: path, Err: errors.ErrUnsupported}
	}
	modTime, changeTime := statTimes(st)
	entry := Entry{
		Mode:       info.Mode(),
		Size:       info.Size(),
		ModTime:    modTime,
		ChangeTime: changeTime,
		Inode:      st.Ino,
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

// Changed returns the paths in cur that are new or different from old, and the
// paths in old that are missing from cur. Both lists are sorted.
func Changed(old, cur Listing) (changed, deleted []string) {
	for path, entry := range cur {
		if previous, ok := old[path]; !ok || previous != entry {
			changed = append(changed, path)
		}
	}
	for path := range old {
		if _, ok := cur[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	slices.Sort(changed)
	slices.Sort(deleted)
	return changed, deleted
}
