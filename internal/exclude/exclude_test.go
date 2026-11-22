package exclude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExcludeDefaultsAndUndoignore(t *testing.T) {
	root := t.TempDir()
	ignore := "# build output\ndist\n!target\n"
	if err := os.WriteFile(filepath.Join(root, ".undoignore"), []byte(ignore), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
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
	}
	for _, c := range cases {
		if got := m.Match(c.rel, c.isDir); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}
