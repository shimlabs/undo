package restore

import (
	"os"
	"path"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

// rewrittenPaths returns the current entries of the paths that differ from
// the target only in their inode and change time, as a path a restore wrote
// back as a new clone does, and that hold the target's content. A file is
// read and compared with its stored copy; one that cannot be read counts as
// changed.
func (p *restorePlan) rewrittenPaths(projectStore *store.Store) (tree.Listing, error) {
	var candidates []string
	for rel, source := range p.target {
		if current, isListed := p.current[rel]; isListed && !source.IsLost && differsOnlyInIdentity(current, source.Entry) {
			candidates = append(candidates, rel)
		}
	}
	if len(candidates) == 0 {
		return tree.Listing{}, nil
	}
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	isSame := make([]bool, len(candidates))
	err := parallel.For(len(candidates), func(i int) error {
		rel := candidates[i]
		if p.current[rel].Kind != tree.File {
			// A symlink's target is in its entry, and other kinds hold no
			// content.
			isSame[i] = true
			return nil
		}
		file, err := openProjectFile(p.root, rel)
		if err != nil {
			return nil
		}
		defer file.Close()
		isSame[i], err = extractor.HasContent(rel, p.target[rel], file)
		if err != nil {
			isSame[i] = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rewritten := tree.Listing{}
	for i, rel := range candidates {
		if isSame[i] {
			rewritten[rel] = p.current[rel]
		}
	}
	return rewritten, nil
}

// differsOnlyInIdentity reports whether current, a file, symlink or named
// pipe, differs from target only in its inode and change time. A write that
// keeps the size and modification time differs the same way, so a file's
// content must still be compared.
func differsOnlyInIdentity(current, target tree.Entry) bool {
	if current == target || current.Kind == tree.Dir {
		return false
	}
	current.Inode, current.ChangeTime = target.Inode, target.ChangeTime
	return current == target
}

// openProjectFile opens the file rel below root for reading, reaching it one
// name at a time and without following a symlink or blocking on a named
// pipe.
func openProjectFile(root, rel string) (*os.File, error) {
	dir, err := tree.OpenDir(root, path.Dir(rel))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), path.Base(rel), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: rel, Err: err}
	}
	return os.NewFile(uintptr(fd), rel), nil
}
