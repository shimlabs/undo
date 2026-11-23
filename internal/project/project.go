// Package project finds the folder undo protects and gives it a stable ID.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Root returns the root of the git repository that contains dir. If dir is
// not inside a repository, it returns dir itself. The result is an absolute
// path with symlinks resolved, spelled the way the filesystem stores it.
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
	for current := resolved; ; {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return resolved, nil
		}
		current = parent
	}
}

// ID returns a stable identifier for a project root.
func ID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:16])
}
