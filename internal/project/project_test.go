package project

import (
	"os"
	"path/filepath"
	"testing"
)

func resolved(t *testing.T, path string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRootFindsGitRoot(t *testing.T) {
	dir := resolved(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := Root(nested)
	if err != nil {
		t.Fatal(err)
	}
	if root != dir {
		t.Fatalf("Root(%q) = %q, want %q", nested, root, dir)
	}
}

func TestRootWithoutGit(t *testing.T) {
	dir := resolved(t, t.TempDir())

	root, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != dir {
		t.Fatalf("Root(%q) = %q, want %q", dir, root, dir)
	}
}

func TestIDStable(t *testing.T) {
	id := ID("/projects/a")
	if id != ID("/projects/a") {
		t.Fatal("ID is not stable for the same root")
	}
	if len(id) != 32 {
		t.Fatalf("len(ID) = %d, want 32", len(id))
	}
	if id == ID("/projects/b") {
		t.Fatal("different roots share an ID")
	}
}

func TestRootUsesStoredSpelling(t *testing.T) {
	parent := resolved(t, t.TempDir())
	stored := filepath.Join(parent, "MyProj")
	if err := os.Mkdir(stored, 0o755); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(parent, "myproj")
	if _, err := os.Stat(typed); err != nil {
		t.Skip("the filesystem is case-sensitive")
	}

	root, err := Root(typed)
	if err != nil {
		t.Fatal(err)
	}
	if root != stored {
		t.Fatalf("Root(%s) = %s, want %s", typed, root, stored)
	}
}
