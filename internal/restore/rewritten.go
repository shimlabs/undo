package restore

import (
	"maps"
	"os"
	"path"
	"slices"

	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

// rewrittenPaths returns the current entries of the paths that differ from
// the target only in their inode and change time, as a path a restore wrote
// back as a new clone does, and that hold the target's content. A file is
// read and compared with its stored copy; one that cannot be read counts as
// changed. A path that still holds what the last restore to this checkpoint
// wrote, as restored records, is known to match and is not read again.
func (p *restorePlan) rewrittenPaths(projectStore *store.Store, restored tree.Listing) tree.Listing {
	candidatesInDir := map[string][]string{}
	for rel, source := range p.target {
		current, isListed := p.current[rel]
		if restoredEntry, isRestored := restored[rel]; isRestored && restoredEntry == current {
			continue
		}
		if isListed && !source.IsLost && differsOnlyInIdentity(current, source.Entry) {
			candidatesInDir[path.Dir(rel)] = append(candidatesInDir[path.Dir(rel)], rel)
		}
	}
	if len(candidatesInDir) == 0 {
		return tree.Listing{}
	}
	// Sorted, so the store's folders are opened in an order its cache keeps.
	dirs := slices.Sorted(maps.Keys(candidatesInDir))
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	isSameInDir := make([][]bool, len(dirs))
	// It never fails: a file that cannot be compared counts as changed.
	_ = parallel.For(len(dirs), func(i int) error {
		candidates := candidatesInDir[dirs[i]]
		isSameInDir[i] = make([]bool, len(candidates))
		dir, err := tree.OpenDir(p.root, dirs[i])
		if err != nil {
			return nil
		}
		defer dir.Close()
		for j, rel := range candidates {
			isSameInDir[i][j] = p.hasTargetContent(extractor, dir, rel)
		}
		return nil
	})
	rewritten := tree.Listing{}
	for i, dir := range dirs {
		for j, rel := range candidatesInDir[dir] {
			if isSameInDir[i][j] {
				rewritten[rel] = p.current[rel]
			}
		}
	}
	return rewritten
}

// hasTargetContent reports whether the path rel, in the open folder dir,
// holds the target's content. A symlink's target is in its entry, and other
// kinds than files hold no content.
func (p *restorePlan) hasTargetContent(extractor *store.Extractor, dir *os.File, rel string) bool {
	if p.current[rel].Kind != tree.File {
		return true
	}
	file, err := tree.OpenFileAt(dir, path.Base(rel))
	if err != nil {
		return false
	}
	defer file.Close()
	isSame, err := extractor.HasContent(rel, p.target[rel], file)
	return err == nil && isSame
}

// differsOnlyInIdentity reports whether current, a file, symlink or named
// pipe, is another file than target that differs from it only in its inode
// and change time. A write that
// keeps the size and modification time differs the same way, so a file's
// content must still be compared.
func differsOnlyInIdentity(current, target tree.Entry) bool {
	// A path a restore wrote back is a new clone. The same file with a new
	// change time may have new extended attributes, which content does not
	// show.
	if current == target || current.Kind == tree.Dir || current.Inode == target.Inode {
		return false
	}
	current.Inode, current.ChangeTime = target.Inode, target.ChangeTime
	return current == target
}
