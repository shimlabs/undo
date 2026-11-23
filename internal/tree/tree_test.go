package tree

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// buildTree creates dirs directories with files files each, some of them
// nested, with an excluded node_modules and a symlink in every directory.
func buildTree(tb testing.TB, dirs, files int) string {
	tb.Helper()
	root := tb.TempDir()
	for d := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", d%7), fmt.Sprintf("mod%d", d))
		if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0o755); err != nil {
			tb.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "node_modules", "dep", "index.js"), nil, 0o644)
		os.Symlink("../shared", filepath.Join(dir, "link"))
		for f := range files {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", f)), []byte("package x"), 0o644)
		}
	}
	return root
}

func walkScan(t *testing.T, root string, ex *exclude.Matcher) (Listing, []string) {
	t.Helper()
	listing := Listing{}
	var excluded []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel != "." && ex.Match(rel, d.IsDir()) {
			excluded = append(excluded, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry, err := Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		listing[rel] = entry
		return nil
	})
	slices.Sort(excluded)
	return listing, excluded
}

func TestScanAllMatchesWalk(t *testing.T) {
	root := buildTree(t, 60, 20)
	ex, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}

	listing, excluded, err := ScanAll(root, ex)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(excluded)

	wantListing, wantExcluded := walkScan(t, root, ex)
	if !maps.Equal(listing, wantListing) {
		changed, deleted := Changed(wantListing, listing)
		t.Fatalf("listing differs: changed %v, deleted %v", changed, deleted)
	}
	if !slices.Equal(excluded, wantExcluded) {
		t.Fatalf("excluded = %v, want %v", excluded, wantExcluded)
	}
}

func TestScanMissingRoot(t *testing.T) {
	ex, _ := exclude.New(t.TempDir())
	listing, err := Scan(filepath.Join(t.TempDir(), "gone"), ex)
	if err != nil || len(listing) != 0 {
		t.Fatalf("Scan = %v, %v; want an empty listing", listing, err)
	}
}

func BenchmarkScan(b *testing.B) {
	root := buildTree(b, 500, 20)
	ex, _ := exclude.New(root)
	for b.Loop() {
		if _, err := Scan(root, ex); err != nil {
			b.Fatal(err)
		}
	}
}
