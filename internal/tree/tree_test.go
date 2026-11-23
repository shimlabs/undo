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

	"github.com/shimlabs/undo/internal/testutil"

	"github.com/shimlabs/undo/internal/exclude"
)

func scan(t *testing.T, root string) Listing {
	t.Helper()
	matcher, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err := Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	return listing
}

func TestScanSkipsExcluded(t *testing.T) {
	root := t.TempDir()
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main")
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "left-pad", "index.js"), "")

	listing := scan(t, root)

	if _, ok := listing["src/main.go"]; !ok {
		t.Fatal("src/main.go missing from listing")
	}
	for path := range listing {
		if path == "node_modules" || strings.HasPrefix(path, "node_modules/") {
			t.Fatalf("excluded path %q in listing", path)
		}
	}
	if listing["."].Kind != Dir {
		t.Fatal("root is not recorded as a directory")
	}
}

func TestScanRecordsSymlinkWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	testutil.WriteFile(t, filepath.Join(outside, "secret.txt"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	listing := scan(t, root)

	entry, ok := listing["link"]
	if !ok || entry.Kind != Symlink || entry.Link != outside {
		t.Fatalf("link entry = %+v, want symlink to %q", entry, outside)
	}
	if _, ok := listing["link/secret.txt"]; ok {
		t.Fatal("scan followed a symlink")
	}
}

func TestChangedDetectsCopyWithPreservedTimes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	testutil.WriteFile(t, path, "aaaa")
	before := scan(t, root)

	info, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	testutil.WriteFile(t, path, "bbbb")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after := scan(t, root)

	changed, _ := Changed(before, after)
	if !slices.Contains(changed, "a.txt") {
		t.Fatalf("changed = %v, want a.txt", changed)
	}
}

func TestChangedReportsDeleted(t *testing.T) {
	root := t.TempDir()
	testutil.WriteFile(t, filepath.Join(root, "keep.txt"), "k")
	testutil.WriteFile(t, filepath.Join(root, "gone.txt"), "g")
	before := scan(t, root)

	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	after := scan(t, root)

	changed, deleted := Changed(before, after)
	if !slices.Contains(deleted, "gone.txt") {
		t.Fatalf("deleted = %v, want gone.txt", deleted)
	}
	if slices.Contains(changed, "keep.txt") {
		t.Fatalf("unchanged keep.txt reported as changed")
	}
}

// buildTree creates dirs directories with files files each, some of them
// nested, with an excluded node_modules and a symlink in every directory.
func buildTree(tb testing.TB, dirs, files int) string {
	tb.Helper()
	root := tb.TempDir()
	for i := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%7), fmt.Sprintf("mod%d", i))
		if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0o755); err != nil {
			tb.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "node_modules", "dep", "index.js"), nil, 0o644)
		os.Symlink("../shared", filepath.Join(dir, "link"))
		for j := range files {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", j)), []byte("package x"), 0o644)
		}
	}
	return root
}

func walkScan(t *testing.T, root string, matcher *exclude.Matcher) (Listing, []string) {
	t.Helper()
	listing := Listing{}
	var excluded []string
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel != "." && matcher.Match(rel, entry.IsDir()) {
			excluded = append(excluded, rel)
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		treeEntry, err := Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		listing[rel] = treeEntry
		return nil
	})
	slices.Sort(excluded)
	return listing, excluded
}

func TestScanWithSkippedMatchesWalkDir(t *testing.T) {
	root := buildTree(t, 60, 20)
	matcher, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}

	listing, skipped, err := Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}

	wantListing, wantExcluded := walkScan(t, root, matcher)
	if !maps.Equal(listing, wantListing) {
		changed, deleted := Changed(wantListing, listing)
		t.Fatalf("listing differs: changed %v, deleted %v", changed, deleted)
	}
	if !slices.Equal(skipped.Excluded, wantExcluded) {
		t.Fatalf("excluded = %v, want %v", skipped.Excluded, wantExcluded)
	}
}

func TestScanSkipsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	root := t.TempDir()
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a")
	testutil.WriteFile(t, filepath.Join(root, "pgdata", "base", "1"), "rows")
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })
	matcher, _ := exclude.New(root)

	listing, skipped, err := Scan(root, matcher)
	if err != nil {
		t.Fatalf("one unreadable directory failed the whole scan: %v", err)
	}
	if _, ok := listing["a.txt"]; !ok {
		t.Fatal("a.txt missing from listing")
	}
	if entry := listing["pgdata"]; entry.Kind != Dir {
		t.Fatalf("pgdata entry = %+v, want the directory itself", entry)
	}
	if !slices.Equal(skipped.Unreadable, []string{"pgdata"}) {
		t.Fatalf("unreadable = %v, want [pgdata]", skipped.Unreadable)
	}
}

func TestScanMissingRoot(t *testing.T) {
	matcher, _ := exclude.New(t.TempDir())
	listing, _, err := Scan(filepath.Join(t.TempDir(), "gone"), matcher)
	if err != nil || len(listing) != 0 {
		t.Fatalf("Scan = %v, %v; want an empty listing", listing, err)
	}
}

func BenchmarkScan(b *testing.B) {
	root := buildTree(b, 500, 20)
	matcher, _ := exclude.New(root)
	for b.Loop() {
		if _, _, err := Scan(root, matcher); err != nil {
			b.Fatal(err)
		}
	}
}

func TestScanSkipsDirectoriesWithoutSearchPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can enter every directory")
	}
	root := t.TempDir()
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a")
	testutil.WriteFile(t, filepath.Join(root, "noexec", "f"), "x")
	os.Chmod(filepath.Join(root, "noexec"), 0o644)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "noexec"), 0o755) })
	matcher, _ := exclude.New(root)

	_, skipped, err := Scan(root, matcher)
	if err != nil {
		t.Fatalf("a directory without search permission failed the scan: %v", err)
	}
	if !slices.Equal(skipped.Unreadable, []string{"noexec"}) {
		t.Fatalf("unreadable = %v, want [noexec]", skipped.Unreadable)
	}
}
