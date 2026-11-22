package store

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func newProject(t *testing.T, files map[string]string) (*Store, string) {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		writeFile(t, filepath.Join(root, path), content)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFirstTakeClonesBase(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha"})

	cp, err := s.Take("first", root)
	if err != nil {
		t.Fatal(err)
	}

	if cp.ID != 1 {
		t.Fatalf("first checkpoint id = %d, want 1", cp.ID)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "base", "a.txt")); got != "alpha" {
		t.Fatalf("base a.txt = %q", got)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Label != "first" || list[0].Dir != root {
		t.Fatalf("List() = %+v", list)
	}
}

func TestTakeRecordsChangedAndDeleted(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	if _, err := s.Take("first", root); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, "a.txt"), "alpha two")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "c", "d.txt"), "delta")

	cp, err := s.Take("second", root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Layer(cp.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"a.txt", "c", "c/d.txt"} {
		if _, ok := m.Changed[path]; !ok {
			t.Errorf("manifest missing changed path %q", path)
		}
	}
	if !slices.Equal(m.Deleted, []string{"b.txt"}) {
		t.Errorf("deleted = %v, want [b.txt]", m.Deleted)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "layers", "2", "files", "a.txt")); got != "alpha two" {
		t.Errorf("layer a.txt = %q", got)
	}
}

func TestTakeIgnoresExcluded(t *testing.T) {
	s, root := newProject(t, map[string]string{"node_modules/pkg/index.js": "v1", "main.js": "x"})
	if _, err := s.Take("first", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "base", "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules was cloned into the base")
	}

	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "v2")
	cp, err := s.Take("second", root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Layer(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Changed) != 0 || len(m.Deleted) != 0 {
		t.Fatalf("excluded change recorded: %+v", m)
	}
}

func TestTakeLockedConcurrently(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha"})
	if _, err := s.Take("first", root); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	ids := make([]int, 2)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := Open(root)
			if err != nil {
				t.Error(err)
				return
			}
			cp, err := other.Take("parallel", root)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = cp.ID
		}()
	}
	wg.Wait()

	slices.Sort(ids)
	if !slices.Equal(ids, []int{2, 3}) {
		t.Fatalf("ids = %v, want [2 3]", ids)
	}
}

func TestFirstTakeNeverReadsExcluded(t *testing.T) {
	s, root := newProject(t, map[string]string{"main.js": "x", "lib/util.js": "u"})
	locked := filepath.Join(root, "node_modules", "locked")
	writeFile(t, filepath.Join(locked, "secret.js"), "s")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	if _, err := s.Take("first", root); err != nil {
		t.Fatalf("first checkpoint read an excluded directory: %v", err)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "base", "lib", "util.js")); got != "u" {
		t.Fatalf("base lib/util.js = %q", got)
	}
}
