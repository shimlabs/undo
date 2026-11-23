package restore

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/store"
)

func setup(t *testing.T) (*store.Store, string) {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"README.md":         "# demo\n",
		"src/main.go":       "package main\n",
		"src/util/str.go":   "package util\n",
		".git/HEAD":         "ref: refs/heads/main\n",
		".git/objects/ab/c": "blob",
	}
	for path, content := range files {
		write(t, filepath.Join(root, path), content)
	}
	if err := os.Chmod(filepath.Join(root, "src", "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/main.go", filepath.Join(root, "entry")); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot records the kind, permissions, modification time and content of
// every path under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			snap[rel] = "link " + target
		case info.IsDir():
			snap[rel] = fmt.Sprintf("dir %v %d", info.Mode().Perm(), info.ModTime().UnixNano())
		default:
			data, _ := os.ReadFile(path)
			snap[rel] = fmt.Sprintf("file %v %d %q", info.Mode().Perm(), info.ModTime().UnixNano(), data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func requireSame(t *testing.T, want, got map[string]string) {
	t.Helper()
	if maps.Equal(want, got) {
		return
	}
	var diff []string
	for path, w := range want {
		if g, ok := got[path]; !ok || g != w {
			diff = append(diff, fmt.Sprintf("%s: want %s, got %s", path, w, g))
		}
	}
	for path, g := range got {
		if _, ok := want[path]; !ok {
			diff = append(diff, fmt.Sprintf("%s: unexpected %s", path, g))
		}
	}
	t.Fatalf("restored tree differs:\n%s", strings.Join(diff, "\n"))
}

func restoreAfter(t *testing.T, damage func(root string)) {
	t.Helper()
	s, root := setup(t)
	want := snapshot(t, root)
	if _, err := s.Take("before damage", root); err != nil {
		t.Fatal(err)
	}

	damage(root)

	if _, err := Apply(s, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreRmRf(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(filepath.Join(root, "src"))
	})
}

func TestRestoreRmRfGit(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(filepath.Join(root, ".git"))
	})
}

func TestRestoreRootDeleted(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(root)
	})
}

func TestRestoreSedInPlace(t *testing.T) {
	restoreAfter(t, func(root string) {
		for _, path := range []string{"README.md", "src/main.go", "src/util/str.go"} {
			full := filepath.Join(root, path)
			data, _ := os.ReadFile(full)
			os.WriteFile(full, []byte(strings.ReplaceAll(string(data), "package", "pkg")), 0o644)
		}
	})
}

func TestRestoreMvOverExisting(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.Rename(filepath.Join(root, "README.md"), filepath.Join(root, "src", "main.go"))
	})
}

func TestRestoreFileBecameDir(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.Remove(filepath.Join(root, "README.md"))
		os.MkdirAll(filepath.Join(root, "README.md", "inner"), 0o755)
		os.WriteFile(filepath.Join(root, "README.md", "inner", "x"), []byte("x"), 0o644)
		os.RemoveAll(filepath.Join(root, "src", "util"))
		os.WriteFile(filepath.Join(root, "src", "util"), []byte("now a file"), 0o644)
	})
}

func TestRestoreKeepsNodeModules(t *testing.T) {
	s, root := setup(t)
	write(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v1")
	if _, err := s.Take("before damage", root); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v2")
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(s, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "main.go")); err != nil {
		t.Fatal("src/main.go not restored")
	}
	data, err := os.ReadFile(filepath.Join(root, "web", "node_modules", "pkg", "index.js"))
	if err != nil || string(data) != "v2" {
		t.Fatalf("excluded file changed by restore: %q, %v", data, err)
	}
}

func TestRestoreIsUndoable(t *testing.T) {
	s, root := setup(t)
	if _, err := s.Take("before damage", root); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))
	write(t, filepath.Join(root, "notes.txt"), "written after the checkpoint")
	damaged := snapshot(t, root)

	if _, err := Apply(s, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, damaged, snapshot(t, root))
}

func TestPlanListsChanges(t *testing.T) {
	s, root := setup(t)
	if _, err := s.Take("before damage", root); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "README.md"))
	write(t, filepath.Join(root, "new.txt"), "new")

	changes, err := Plan(s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(changes.Write, "README.md") || !contains(changes.Remove, "new.txt") {
		t.Fatalf("Plan = %+v", changes)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestRestoreDirEditedAfterBase(t *testing.T) {
	s, root := setup(t)
	if _, err := s.Take("first", root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "src", "main.go"), "package main // edited\n")
	want := snapshot(t, root)
	if _, err := s.Take("after edit", root); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreDirKeepsEarlierDeletes(t *testing.T) {
	s, root := setup(t)
	if _, err := s.Take("first", root); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "src", "util", "str.go"))
	want := snapshot(t, root)
	if _, err := s.Take("after delete", root); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}
