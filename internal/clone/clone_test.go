package clone

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestFileAtRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyByPath(t, FileAt, src, dst); err == nil {
		t.Fatal("FileAt overwrote an existing destination")
	}
	testutil.AssertContent(t, dst, "old")
}

// sourceTree builds a directory with a nested file, an executable, a symlink
// that points outside it, and old modification times.
func sourceTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, rel := range []string{"a/f.txt", "run.sh", "a", "."} {
		if err := os.Chtimes(filepath.Join(src, rel), old, old); err != nil {
			t.Fatal(err)
		}
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
	testutil.AssertContent(t, filepath.Join(dst, "a", "f.txt"), "hello")
	if target, _ := os.Readlink(filepath.Join(dst, "link")); target != "/etc/hosts" {
		t.Errorf("link points to %q", target)
	}
}

func TestFileAtClonesDirectories(t *testing.T) {
	src := sourceTree(t)
	dst := filepath.Join(filepath.Dir(src), "dst")
	if err := copyByPath(t, FileAt, src, dst); err != nil {
		t.Fatal(err)
	}
	checkSameTree(t, src, dst)
}

func TestCopyAtMatchesSource(t *testing.T) {
	src := sourceTree(t)
	dst := filepath.Join(filepath.Dir(src), "dst")
	if err := copyByPath(t, CopyAt, src, dst); err != nil {
		t.Fatal(err)
	}
	checkSameTree(t, src, dst)

	if err := copyByPath(t, CopyAt, src, dst); err == nil {
		t.Fatal("CopyAt overwrote an existing destination")
	}
}

func TestMkfifoAtCreatesPipeInDirectory(t *testing.T) {
	dirPath := t.TempDir()
	dir, err := os.Open(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	if err := MkfifoAt(dir, "pipe", 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dirPath, "pipe"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeNamedPipe == 0 || info.Mode().Perm() != 0o640&^umask() {
		t.Fatalf("pipe mode = %v", info.Mode())
	}
}

// umask returns the process's file mode creation mask.
func umask() fs.FileMode {
	mask := syscall.Umask(0)
	syscall.Umask(mask)
	return fs.FileMode(mask)
}

// copyByPath calls copyFile with the parent directories of src and dst
// opened.
func copyByPath(t *testing.T, copyFile func(srcDir *os.File, srcName string, dir *os.File, name string) error, src, dst string) error {
	t.Helper()
	srcDir, err := os.Open(filepath.Dir(src))
	if err != nil {
		t.Fatal(err)
	}
	defer srcDir.Close()
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	return copyFile(srcDir, filepath.Base(src), dir, filepath.Base(dst))
}

func TestFileAtNeverFollowsSymlinkInSourceName(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if err := copyByPath(t, FileAt, filepath.Join(root, "link"), filepath.Join(root, "copy")); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(root, "copy")); err != nil || target != outside {
		t.Fatalf("copy = %q, %v; want a symlink to %s", target, err, outside)
	}
}

// Only where macOS makes "._" files may an existing one be replaced; a "._"
// name elsewhere is an ordinary file, link or pipe like any other.
func TestCopyAtNeverOpensExistingAppleDoubleName(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	testutil.WriteFile(t, filepath.Join(src, "._x"), "payload")
	victim := filepath.Join(outside, "id_ed25519")
	testutil.WriteFile(t, victim, "secret")
	if err := os.Chmod(victim, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(dst, "._x")); err != nil {
		t.Fatal(err)
	}
	if err := copyByPath(t, CopyAt, filepath.Join(src, "._x"), filepath.Join(dst, "._x")); err == nil {
		t.Error("CopyAt wrote onto an existing ._x")
	}
	testutil.AssertContent(t, victim, "secret")
	if info, err := os.Stat(victim); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("victim mode = %v, %v; want -rw-------", info.Mode(), err)
	}

	if err := syscall.Mkfifo(filepath.Join(dst, "._p"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(src, "._p"), "payload")
	finished := make(chan error, 1)
	go func() { finished <- copyByPath(t, CopyAt, filepath.Join(src, "._p"), filepath.Join(dst, "._p")) }()
	select {
	case err := <-finished:
		if err == nil {
			t.Error("CopyAt wrote into an existing ._p named pipe")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CopyAt blocked on an existing ._p named pipe")
	}
}
