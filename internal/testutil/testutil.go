// Package testutil holds helpers shared by the tests of other packages.
package testutil

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// WriteFile writes content to path, creating its parent directories.
func WriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Fingerprint maps every path under root to whether it is a directory and a
// hash of its content, so two trees can be compared with maps.Equal.
func Fingerprint(t testing.TB, root string) map[string]string {
	t.Helper()
	fingerprints := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		var content []byte
		if !entry.IsDir() {
			if content, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		fingerprints[rel] = fmt.Sprintf("%v %x", entry.IsDir(), sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fingerprints
}

// TempDir returns a new temporary directory with symlinks in its path
// resolved, as undo resolves project roots.
func TempDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
