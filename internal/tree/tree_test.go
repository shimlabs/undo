package tree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, root string) Listing {
	t.Helper()
	m, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Scan(root, m)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestScanSkipsExcluded(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "src", "main.go"), "package main")
	write(t, filepath.Join(root, "node_modules", "left-pad", "index.js"), "")

	l := scan(t, root)

	if _, ok := l["src/main.go"]; !ok {
		t.Fatal("src/main.go missing from listing")
	}
	for path := range l {
		if path == "node_modules" || strings.HasPrefix(path, "node_modules/") {
			t.Fatalf("excluded path %q in listing", path)
		}
	}
	if l["."].Kind != Dir {
		t.Fatal("root is not recorded as a directory")
	}
}

func TestScanRecordsSymlinkWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	l := scan(t, root)

	e, ok := l["link"]
	if !ok || e.Kind != Symlink || e.Link != outside {
		t.Fatalf("link entry = %+v, want symlink to %q", e, outside)
	}
	if _, ok := l["link/secret.txt"]; ok {
		t.Fatal("scan followed a symlink")
	}
}

func TestChangedDetectsCpPreservedTimes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	write(t, path, "aaaa")
	before := scan(t, root)

	info, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	write(t, path, "bbbb")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after := scan(t, root)

	changed, _ := Changed(before, after)
	if !contains(changed, "a.txt") {
		t.Fatalf("changed = %v, want a.txt", changed)
	}
}

func TestChangedReportsDeleted(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "keep.txt"), "k")
	write(t, filepath.Join(root, "gone.txt"), "g")
	before := scan(t, root)

	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	after := scan(t, root)

	changed, deleted := Changed(before, after)
	if !contains(deleted, "gone.txt") {
		t.Fatalf("deleted = %v, want gone.txt", deleted)
	}
	if contains(changed, "keep.txt") {
		t.Fatalf("unchanged keep.txt reported as changed")
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
