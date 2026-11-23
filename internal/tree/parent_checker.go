package tree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

// ErrParentNotDir means a directory above a path is now a symlink or a file,
// so acting on the path would follow it, possibly out of the tree.
var ErrParentNotDir = errors.New("a parent is not a directory")

// ParentChecker checks that the parents of paths under root are real
// directories. It remembers the directories it checked, so callers that
// replace a directory must call Forget.
type ParentChecker struct {
	root     string
	verified map[string]bool
}

// NewParentChecker returns a ParentChecker for paths under root.
func NewParentChecker(root string) *ParentChecker {
	return &ParentChecker{root: root, verified: map[string]bool{}}
}

// Check returns an error wrapping ErrParentNotDir if a parent of rel is not a
// real directory. Parents that do not exist pass, since creating them makes
// real directories.
func (c *ParentChecker) Check(rel string) error {
	for dir := path.Dir(rel); dir != "." && rel != "."; dir = path.Dir(dir) {
		if c.verified[dir] {
			continue
		}
		info, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(dir)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP):
			// A directory further up is a file or a symlink.
			return fmt.Errorf("%s: %w", dir, ErrParentNotDir)
		case err != nil:
			return err
		case !info.IsDir():
			return fmt.Errorf("%s: %w", dir, ErrParentNotDir)
		}
		c.verified[dir] = true
	}
	return nil
}

// Forget drops what the checker remembers about rel, after rel was removed
// or replaced.
func (c *ParentChecker) Forget(rel string) {
	delete(c.verified, rel)
}
