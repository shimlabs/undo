// Package tree lists the files in a project and finds what changed between
// two listings.
package tree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/shimlabs/undo/internal/exclude"
)

// Kind is the type of a filesystem entry.
type Kind uint8

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
	listing, _, err := ScanAll(root, ex)
	return listing, err
}

// ScanAll works like Scan and also returns the excluded paths it skipped. It
// does not go into excluded directories.
func ScanAll(root string, ex *exclude.Matcher) (Listing, []string, error) {
	listing := Listing{}
	var excluded []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && ex.Match(rel, d.IsDir()) {
			excluded = append(excluded, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry, err := Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		listing[rel] = entry
		return nil
	})
	return listing, excluded, err
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
	modTime, changeTime := times(st)
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
