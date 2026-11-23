package exclude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newMatcher(t *testing.T, undoignore string) *Matcher {
	t.Helper()
	root := t.TempDir()
	if undoignore != "" {
		if err := os.WriteFile(filepath.Join(root, ".undoignore"), []byte(undoignore), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	matcher, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func TestMatchDefaultsAndUndoignore(t *testing.T) {
	matcher := newMatcher(t, "# build output\ndist\n!target\n")

	for _, tc := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"node_modules", true, true},
		{"web/node_modules", true, true},
		{"dist", true, true},
		{"target", true, false},
		{"src", true, false},
		{"config.local.json", false, false},
	} {
		if got := matcher.Match(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestDefaultsSkipOnlyDirectories(t *testing.T) {
	matcher := newMatcher(t, "")

	for _, name := range []string{"target", ".cache", "venv"} {
		if matcher.Match(name, false) {
			t.Errorf("a file named %q is excluded, but only directories by that name should be", name)
		}
		if !matcher.Match(name, true) {
			t.Errorf("a directory named %q is not excluded", name)
		}
	}
}

func TestExcludesChecksParentDirectories(t *testing.T) {
	matcher := newMatcher(t, "build/\n")

	for _, tc := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"build", true, true},
		{"build/out.bin", false, true},
		{"web/build/assets/app.js", false, true},
		{"web/node_modules/react/index.js", false, true},
		{"build", false, false},
		{"src/build.go", false, false},
		{".", true, false},
	} {
		if got := matcher.Excludes(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
		}
	}
}

func TestNewRefusesIgnoreFileThatIsNotARegularFile(t *testing.T) {
	for name, create := range map[string]func(path string) error{
		"device": func(path string) error { return os.Symlink("/dev/urandom", path) },
		"pipe":   func(path string) error { return syscall.Mkfifo(path, 0o600) },
	} {
		root := t.TempDir()
		if err := create(filepath.Join(root, IgnoreFile)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := New(root)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrIgnoreFileNotRegular) {
				t.Errorf("%s: New = %v, want ErrIgnoreFileNotRegular", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: New did not return", name)
		}
	}
}

func TestNewRefusesHugeIgnoreFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, IgnoreFile), []byte(strings.Repeat("a\n", maxIgnoreFileSize)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root); !errors.Is(err, ErrIgnoreFileTooLarge) {
		t.Fatalf("New = %v, want ErrIgnoreFileTooLarge", err)
	}
}

func TestNewFollowsSymlinkToIgnoreFile(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared.undoignore")
	if err := os.WriteFile(shared, []byte("dist\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, IgnoreFile)); err != nil {
		t.Fatal(err)
	}
	matcher, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if !matcher.Match("dist", true) {
		t.Fatal("dist is not excluded")
	}
}
