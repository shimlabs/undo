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

	"golang.org/x/sys/unix"
)

// WriteFile writes content to path, creating its parent directories.
func WriteFile(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatal(err)
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
func Fingerprint(tb testing.TB, root string) map[string]string {
	tb.Helper()
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
		tb.Fatal(err)
	}
	return fingerprints
}

// TempDir returns a new temporary directory with symlinks in its path
// resolved, as undo resolves project roots.
func TempDir(tb testing.TB) string {
	tb.Helper()
	dir, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	return dir
}

// AgentHome points every agent's configuration at a fresh home directory and
// returns it.
func AgentHome(tb testing.TB) string {
	tb.Helper()
	home := tb.TempDir()
	tb.Setenv("HOME", home)
	tb.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	tb.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	tb.Setenv("GEMINI_CLI_HOME", home)
	tb.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// PrivateBinary returns the path of an undo binary in dirName, a directory
// only the test user can write to and outside the temporary directory, both
// of which installing a hook requires. It moves TMPDIR aside so the test's
// own directories qualify.
func PrivateBinary(tb testing.TB, dirName string) string {
	tb.Helper()
	parent := tb.TempDir()
	tb.Setenv("TMPDIR", filepath.Join(parent, "tmp"))
	if err := os.MkdirAll(filepath.Join(parent, "tmp"), 0o700); err != nil {
		tb.Fatal(err)
	}
	dir := filepath.Join(parent, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	return filepath.Join(dir, "undo")
}

// History makes four changes to the project at root, calling checkpoint
// after each, and returns the project's fingerprint at each checkpoint. The
// changes add, delete and replace files and turn a directory into a file.
func History(tb testing.TB, root string, checkpoint func(label string)) []map[string]string {
	tb.Helper()
	steps := []func(){
		func() { WriteFile(tb, filepath.Join(root, "a.txt"), "one") },
		func() { WriteFile(tb, filepath.Join(root, "dir", "b.txt"), "two") },
		func() { removeAll(tb, filepath.Join(root, "a.txt")) },
		func() {
			removeAll(tb, filepath.Join(root, "dir"))
			WriteFile(tb, filepath.Join(root, "dir"), "now a file")
		},
	}
	states := make([]map[string]string, 0, len(steps))
	for i, step := range steps {
		step()
		states = append(states, Fingerprint(tb, root))
		checkpoint(fmt.Sprintf("step %d", i+1))
	}
	return states
}

func removeAll(tb testing.TB, path string) {
	tb.Helper()
	if err := os.RemoveAll(path); err != nil {
		tb.Fatal(err)
	}
}

// SetOpenFileLimit lowers the soft limit on open files for the rest of the
// test, so code that opens a descriptor per path fails within reach.
func SetOpenFileLimit(tb testing.TB, limit uint64) {
	tb.Helper()
	var saved unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &saved); err != nil {
		tb.Fatal(err)
	}
	lowered := saved
	lowered.Cur = limit
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &lowered); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &saved); err != nil {
			tb.Error(err)
		}
	})
}

// SocketDir returns a new directory for a watcher's socket, removed when the
// test ends. A socket path must fit in 104 bytes, which the test's own
// temporary directory does not on macOS.
func SocketDir(tb testing.TB) string {
	tb.Helper()
	dir, err := os.MkdirTemp("/tmp", "undo-test-") //nolint:usetesting // t.TempDir paths are too long for a socket on macOS
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// SkipIfRoot skips a test that needs permissions to hold, which root
// ignores.
func SkipIfRoot(tb testing.TB) {
	tb.Helper()
	if os.Geteuid() == 0 {
		tb.Skip("root ignores permissions")
	}
}

// PlainDir returns a new folder on the filesystem without clones that
// UNDO_TEST_PLAIN_DIR names, and skips the test when it is not set. The
// Linux CI job points it at ext4 or overlayfs.
func PlainDir(tb testing.TB) string {
	tb.Helper()
	plain := os.Getenv("UNDO_TEST_PLAIN_DIR")
	if plain == "" {
		tb.Skip("UNDO_TEST_PLAIN_DIR is not set")
	}
	dir, err := os.MkdirTemp(plain, "project-") //nolint:usetesting // the folder must be on the filesystem UNDO_TEST_PLAIN_DIR names
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// AssertContent fails the test unless the file at path holds want.
func AssertContent(tb testing.TB, path, want string) {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		tb.Fatalf("%s holds %q, %v; want %q", path, data, err, want)
	}
}
