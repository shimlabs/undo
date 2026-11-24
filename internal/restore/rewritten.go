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
			dir := path.Dir(rel)
			candidatesInDir[dir] = append(candidatesInDir[dir], rel)
		}
	}
	if len(candidatesInDir) == 0 {
		return tree.Listing{}
	}
	batches := compareBatches(candidatesInDir)
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	isSameInBatch := make([][]bool, len(batches))
	// It never fails: a file that cannot be compared counts as changed.
	_ = parallel.For(len(batches), func(i int) error {
		batch := batches[i]
		isSameInBatch[i] = make([]bool, len(batch.paths))
		dir, err := tree.OpenDir(p.root, batch.dir)
		if err != nil {
			return nil
		}
		defer dir.Close()
		for j, rel := range batch.paths {
			isSameInBatch[i][j] = p.hasTargetContent(extractor, dir, rel)
		}
		return nil
	})
	rewritten := tree.Listing{}
	for i, batch := range batches {
		for j, rel := range batch.paths {
			if isSameInBatch[i][j] {
				rewritten[rel] = p.current[rel]
			}
		}
	}
	return rewritten
}

// compareBatchSize is the most paths one worker compares in a folder it
// opens once, so a large folder is still compared on every core.
const compareBatchSize = 64

// compareBatch is paths in the folder dir that one worker compares.
type compareBatch struct {
	dir   string
	paths []string
}

// compareBatches splits the paths in each folder into batches, with the
// folders sorted so the store's folders are opened in an order its cache
// keeps.
func compareBatches(pathsInDir map[string][]string) []compareBatch {
	var batches []compareBatch
	for _, dir := range slices.Sorted(maps.Keys(pathsInDir)) {
		for paths := range slices.Chunk(pathsInDir[dir], compareBatchSize) {
			batches = append(batches, compareBatch{dir: dir, paths: paths})
		}
	}
	return batches
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
// pipe, differs from target only in its inode and change time, as a file a
// restore wrote back does. A file saved by replacing it, keeping its size and
// modification time, differs the same way, so a file's content must still be
// compared.
func differsOnlyInIdentity(current, target tree.Entry) bool {
	// A path a restore wrote back is a new clone, though XFS may give it the
	// inode number the checkpoint recorded.
	if current == target || current.Kind == tree.Dir || (hasExtendedAttributesInClones && current.Inode == target.Inode) {
		return false
	}
	current.Inode, current.ChangeTime = target.Inode, target.ChangeTime
	return current == target
}
