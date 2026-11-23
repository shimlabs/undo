// Package project finds the folder undo protects and gives it a stable ID.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNotProject means the folder is a home directory or the root of the
// disk, which undo does not protect as a project.
var ErrNotProject = errors.New("this is a home directory or the disk root, not a project folder")

// Root returns the root of the git repository that contains dir. If dir is
// not inside a repository, it returns dir itself. The result is an absolute
// path with symlinks resolved, spelled the way the filesystem stores it.
//
// The search stops below the user's home directory, since a home directory
// kept in git for dotfiles is not a project, and it ignores a .git that
// belongs to another user, which anyone could create in a shared directory
// such as /tmp. It returns an error wrapping ErrNotProject when the result
// would be the home directory or the disk root.
func Root(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved, err = canonicalCase(resolved); err != nil {
		return "", err
	}
	userHome := resolvedUserHome()
	for current := resolved; current != userHome; {
		if isOwnGitDir(filepath.Join(current, ".git")) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return resolved, Check(resolved)
}

// Check returns an error wrapping ErrNotProject if root is the user's home
// directory or the disk root. root must have its symlinks resolved.
func Check(root string) error {
	if root == "/" || root == resolvedUserHome() {
		return fmt.Errorf("%s: %w", root, ErrNotProject)
	}
	return nil
}

func resolvedUserHome() string {
	userHome, _ := os.UserHomeDir()
	if resolved, err := filepath.EvalSymlinks(userHome); err == nil {
		return resolved
	}
	return userHome
}

func isOwnGitDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// ID returns a stable identifier for a project root.
func ID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:16])
}
