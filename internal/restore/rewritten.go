package restore

import (
	"path"

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
	var candidates []string
	for rel, source := range p.target {
		current, isListed := p.current[rel]
		if restoredEntry, isRestored := restored[rel]; isRestored && restoredEntry == current {
			continue
		}
		if isListed && !source.IsLost && differsOnlyInIdentity(current, source.Entry) {
			candidates = append(candidates, rel)
		}
	}
	if len(candidates) == 0 {
		return tree.Listing{}
	}
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	isSame := make([]bool, len(candidates))
	// It never fails: a file that cannot be compared counts as changed.
	_ = parallel.For(len(candidates), func(i int) error {
		rel := candidates[i]
		if p.current[rel].Kind != tree.File {
			// A symlink's target is in its entry, and other kinds hold no
			// content.
			isSame[i] = true
			return nil
		}
		dir, err := tree.OpenDir(p.root, path.Dir(rel))
		if err != nil {
			return nil
		}
		defer dir.Close()
		file, err := tree.OpenFileAt(dir, path.Base(rel))
		if err != nil {
			return nil
		}
		defer file.Close()
		isSame[i], _ = extractor.HasContent(rel, p.target[rel], file)
		return nil
	})
	rewritten := tree.Listing{}
	for i, rel := range candidates {
		if isSame[i] {
			rewritten[rel] = p.current[rel]
		}
	}
	return rewritten
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
