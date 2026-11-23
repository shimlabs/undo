// Package testutil holds helpers shared by the tests of other packages.
package testutil

import (
	"crypto/sha256"
	"errors"
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

// RestoreModeOnCleanup sets path back to mode when the test ends, so a test
// that takes away permissions leaves a tree its cleanup can remove. A path
// the test already removed is fine.
func RestoreModeOnCleanup(tb testing.TB, path string, mode fs.FileMode) {
	tb.Helper()
	tb.Cleanup(func() {
		if err := os.Chmod(path, mode); err != nil && !errors.Is(err, fs.ErrNotExist) {
			tb.Error(err)
		}
	})
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

// AgentHome points every agent's configuration at a fresh home directory and
// returns it.
func AgentHome(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// PrivateBinary returns the path of an undo binary in dirName, a directory
// only the test user can write to and outside the temporary directory, both
// of which installing a hook requires. It moves TMPDIR aside so the test's
// own directories qualify.
func PrivateBinary(t testing.TB, dirName string) string {
	t.Helper()
	parent := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(parent, "tmp"))
	if err := os.MkdirAll(filepath.Join(parent, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "undo")
}

// History makes four changes to the project at root, calling checkpoint
// after each, and returns the project's fingerprint at each checkpoint. The
// changes add, delete and replace files and turn a directory into a file.
func History(t testing.TB, root string, checkpoint func(label string)) []map[string]string {
	t.Helper()
	steps := []func(){
		func() { WriteFile(t, filepath.Join(root, "a.txt"), "one") },
		func() { WriteFile(t, filepath.Join(root, "dir", "b.txt"), "two") },
		func() { removeAll(t, filepath.Join(root, "a.txt")) },
		func() {
			removeAll(t, filepath.Join(root, "dir"))
			WriteFile(t, filepath.Join(root, "dir"), "now a file")
		},
	}
	var states []map[string]string
	for i, step := range steps {
		step()
		states = append(states, Fingerprint(t, root))
		checkpoint(fmt.Sprintf("step %d", i+1))
	}
	return states
}

func removeAll(t testing.TB, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}
