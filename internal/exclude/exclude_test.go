package exclude

import (
	"os"
	"path/filepath"
	"testing"
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
