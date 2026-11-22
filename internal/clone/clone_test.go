package clone

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneFileRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := File(src, dst); err == nil {
		t.Fatal("File overwrote an existing destination")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "old" {
		t.Fatalf("destination changed to %q", data)
	}
}

// sourceTree builds a directory with a nested file, an executable, a symlink
// that points outside it, and old modification times.
func sourceTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a", "f.txt"), []byte("hello"), 0o600)
	os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh"), 0o755)
	os.Symlink("/etc/hosts", filepath.Join(src, "link"))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, rel := range []string{"a/f.txt", "run.sh", "a", "."} {
		os.Chtimes(filepath.Join(src, rel), old, old)
	}
	return src
}

func checkSameTree(t *testing.T, src, dst string) {
	t.Helper()
	for _, rel := range []string{".", "a", "a/f.txt", "run.sh", "link"} {
		want, err := os.Lstat(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.Lstat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
		if got.Mode() != want.Mode() {
			t.Errorf("%s mode = %v, want %v", rel, got.Mode(), want.Mode())
		}
		if got.Mode().IsRegular() {
			if !got.ModTime().Equal(want.ModTime()) {
				t.Errorf("%s mtime = %v, want %v", rel, got.ModTime(), want.ModTime())
			}
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "a", "f.txt")); string(data) != "hello" {
		t.Errorf("a/f.txt = %q", data)
	}
	if target, _ := os.Readlink(filepath.Join(dst, "link")); target != "/etc/hosts" {
		t.Errorf("link points to %q", target)
	}
}

func TestCloneFileClonesDirectories(t *testing.T) {
	src := sourceTree(t)
	dst := filepath.Join(filepath.Dir(src), "dst")
	if err := File(src, dst); err != nil {
		t.Fatal(err)
	}
	checkSameTree(t, src, dst)
}

func TestCopyMatchesSource(t *testing.T) {
	src := sourceTree(t)
	dst := filepath.Join(filepath.Dir(src), "dst")
	if err := Copy(src, dst); err != nil {
		t.Fatal(err)
	}
	checkSameTree(t, src, dst)

	if err := Copy(src, dst); err == nil {
		t.Fatal("Copy overwrote an existing destination")
	}
}
