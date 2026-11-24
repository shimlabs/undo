package tree

import (
	"maps"
	"path"
	"slices"

	"github.com/shimlabs/undo/internal/exclude"
)

// AddMissed adds to current each path of previous that current lacks but
// that still exists, with what it holds if it is a directory. A directory
// read can leave out a name that a rename replaces while it runs, as Btrfs
// does, and a path recorded as deleted would be removed by a restore.
func AddMissed(root string, matcher *exclude.Matcher, previous, current Listing) error {
	for _, rel := range slices.Sorted(maps.Keys(previous)) {
		if _, isListed := current[rel]; isListed || rel == "." {
			continue
		}
		if parent, isListed := current[path.Dir(rel)]; !isListed || parent.Kind != Dir {
			continue
		}
		entry, isFound := lookUpMissed(root, rel)
		if !isFound || matcher.Excludes(rel, entry.Kind == Dir) {
			continue
		}
		current[rel] = entry
		if entry.Kind == Dir {
			found, _, err := ScanDir(root, rel, matcher)
			if err != nil {
				return err
			}
			maps.Copy(current, found)
		}
	}
	return nil
}

// lookUpMissed describes rel, reached one name at a time from root, if it
// exists under the same spelling. On a filesystem that ignores case, a stat
// finds a name in any spelling, and only the stored one is the same path.
func lookUpMissed(root, rel string) (Entry, bool) {
	dir, err := OpenDir(root, path.Dir(rel))
	if err != nil {
		return Entry{}, false
	}
	defer dir.Close()
	entry, err := StatAt(dir, path.Base(rel))
	if err != nil {
		return Entry{}, false
	}
	storedName, err := StoredNameAt(dir, path.Base(rel))
	return entry, err == nil && storedName == path.Base(rel)
}
