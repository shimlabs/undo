package store

import (
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/shimlabs/undo/internal/appledouble"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/tree"
	"github.com/shimlabs/undo/internal/walk"
)

// cloneBase clones the project into the base and returns the listing taken
// just before the clone. If a path changes while the clone runs, it no longer
// matches that listing, so the first checkpoint records it again. Excluded
// and unreadable paths are never read.
func (s *Store) cloneBase(matcher *exclude.Matcher) (tree.Listing, error) {
	before, skipped, err := tree.Scan(s.root, matcher)
	if err != nil {
		return nil, err
	}
	base := s.baseDir()
	if err := s.removeAll(base); err != nil {
		return nil, err
	}
	plan := basePlan{
		hasSkippedDescendant: map[string]bool{},
		children:             map[string][]string{},
		listing:              before,
	}
	for _, rel := range skipped.Excluded {
		plan.markAncestors(rel)
	}
	for _, rel := range skipped.Unreadable {
		plan.markAncestors(rel)
		plan.hasSkippedDescendant[rel] = true
	}
	for rel := range before {
		if rel != "." {
			plan.children[path.Dir(rel)] = append(plan.children[path.Dir(rel)], rel)
		}
	}
	if err := s.cloneInto(".", &plan); err != nil {
		return nil, errors.Join(toCloneError(err), s.removeAll(base))
	}
	// The listing records each directory's mode. In the base, read-only
	// directories would stop merges and keep the store from being deleted.
	for rel, entry := range before {
		if entry.Kind == tree.Dir && entry.Mode.Perm()&0o700 != 0o700 {
			if err := s.makeWritable(s.basePath(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	// A file created after the scan and removed before the first checkpoint
	// would otherwise stay in the base, and a restore that clones a whole
	// directory from the base would bring it back.
	if err := s.removeUnlisted(before); err != nil {
		return nil, err
	}
	if err := s.writeGob(s.baseListingPath(), before); err != nil {
		return nil, err
	}
	return before, nil
}

// removeUnlisted removes every path in the base that listing does not hold,
// except, where macOS keeps extended attributes in "._" files, the "._" file
// of a listed path.
func (s *Store) removeUnlisted(listing tree.Listing) error {
	hasAppleDoubleFiles := appledouble.IsUsedIn(s.baseDir())
	return walk.Dirs(".", func(dir string) ([]string, error) {
		entries, err := s.readDir(s.basePath(dir))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var subdirs []string
		for _, entry := range entries {
			rel := entry.Name()
			if dir != "." {
				rel = dir + "/" + entry.Name()
			}
			listed, isListed := listing[rel]
			switch {
			case hasAppleDoubleFiles && isAppleDoubleOfListed(listing, dir, entry.Name()):
				// It holds the extended attributes of the file beside it.
			case !isListed:
				if err := s.removeAll(s.basePath(rel)); err != nil {
					return nil, err
				}
			case listed.Kind == tree.Dir && entry.IsDir():
				subdirs = append(subdirs, rel)
			}
		}
		return subdirs, nil
	})
}

// isAppleDoubleOfListed reports whether name, in dir, is the "._" file of a
// listed path in dir.
func isAppleDoubleOfListed(listing tree.Listing, dir, name string) bool {
	owner, isAppleDouble := strings.CutPrefix(name, appledouble.Prefix)
	if !isAppleDouble {
		return false
	}
	_, isListed := listing[path.Join(dir, owner)]
	return isListed
}

// basePlan describes how to clone the project into the base: directories
// with skipped paths below them are created and filled child by child, and
// everything else is cloned in one call.
type basePlan struct {
	hasSkippedDescendant map[string]bool
	children             map[string][]string
	listing              tree.Listing
}

func (p *basePlan) markAncestors(rel string) {
	for dir := path.Dir(rel); !p.hasSkippedDescendant[dir]; dir = path.Dir(dir) {
		p.hasSkippedDescendant[dir] = true
		if dir == "." {
			return
		}
	}
}

// cloneInto clones rel into the base. A directory with a skipped path below
// it, excluded or unreadable, is created, and everything else below it is
// cloned whole, many at a time. A directory that turns out to hold something
// unreadable is cloned again child by child, and the unreadable files are
// left out of the listing.
func (s *Store) cloneInto(rel string, plan *basePlan) error {
	if rel == "." && !plan.hasSkippedDescendant[rel] {
		err := s.cloneIntoStore(rel, s.basePath(rel))
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}
		if err := s.removeAll(s.basePath(rel)); err != nil {
			return err
		}
		plan.hasSkippedDescendant[rel] = true
	}
	whole, err := s.makeBaseDirs(rel, plan)
	if err != nil {
		return err
	}
	unreadable, err := s.cloneWhole(whole)
	if err != nil {
		return err
	}
	for _, child := range unreadable {
		if plan.listing[child].Kind != tree.Dir {
			delete(plan.listing, child)
			continue
		}
		if err := s.removeAll(s.basePath(child)); err != nil {
			return err
		}
		plan.hasSkippedDescendant[child] = true
		if err := s.cloneInto(child, plan); err != nil {
			return err
		}
	}
	return nil
}

// makeBaseDirs creates rel in the base, and every directory below it that
// has a skipped path below it, and returns the paths to clone whole.
func (s *Store) makeBaseDirs(rel string, plan *basePlan) ([]string, error) {
	if !plan.hasSkippedDescendant[rel] {
		return []string{rel}, nil
	}
	if err := s.mkdirAll(s.basePath(rel)); err != nil {
		return nil, err
	}
	var whole []string
	for _, child := range plan.children[rel] {
		childWhole, err := s.makeBaseDirs(child, plan)
		if err != nil {
			return nil, err
		}
		whole = append(whole, childWhole...)
	}
	return whole, nil
}

// cloneWhole clones each path into the base in one call, in batches of at
// most maxOpenDirs parent directories, and returns the paths it could not
// read.
func (s *Store) cloneWhole(paths []string) ([]string, error) {
	var mu sync.Mutex
	var unreadable []string
	for _, batch := range batchByDir(paths, maxOpenDirs) {
		err := s.cloneWholeBatch(paths, batch, func(rel string) {
			mu.Lock()
			defer mu.Unlock()
			unreadable = append(unreadable, rel)
		})
		if err != nil {
			return nil, err
		}
	}
	return unreadable, nil
}

// cloneWholeBatch clones paths[i] for each i in batch, opening each parent
// once in the project and in the base, and calls onUnreadable for a path it
// has no permission to read.
func (s *Store) cloneWholeBatch(paths []string, batch []int, onUnreadable func(rel string)) error {
	dirs, err := openDirPairs(paths, batch, s.openBasePair)
	if err != nil {
		return err
	}
	defer closeDirPairs(dirs)
	return parallel.For(len(batch), func(j int) error {
		rel := paths[batch[j]]
		pair, isOpen := dirs[path.Dir(rel)]
		if !isOpen {
			return nil
		}
		err := s.cloneAt(pair.project, path.Base(rel), pair.stored, path.Base(rel))
		switch {
		case errors.Is(err, fs.ErrPermission):
			onUnreadable(rel)
			return nil
		case errors.Is(err, fs.ErrNotExist):
			return nil
		}
		return err
	})
}

// openBasePair opens the directory dir in the project, one name at a time,
// and in the base. A project directory gone since the scan matches
// fs.ErrNotExist.
func (s *Store) openBasePair(dir string) (dirPair, error) {
	projectDir, err := tree.OpenDir(s.root, dir)
	if err != nil {
		return dirPair{}, err
	}
	baseDir, err := s.open(s.basePath(dir))
	if err != nil {
		return dirPair{}, errors.Join(err, projectDir.Close())
	}
	return dirPair{project: projectDir, stored: baseDir}, nil
}
