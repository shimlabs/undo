package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestRootFindsGitRoot(t *testing.T) {
	dir := testutil.TempDir(t)
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
	dir := testutil.TempDir(t)

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
	parent := testutil.TempDir(t)
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

func TestRootStopsBelowHome(t *testing.T) {
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "notes")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != dir {
		t.Fatalf("Root(%s) = %s, want the folder itself, not a home directory kept in git", dir, root)
	}
}

func TestRootRefusesHomeAndDiskRoot(t *testing.T) {
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	for _, dir := range []string{home, "/"} {
		if _, err := Root(dir); !errors.Is(err, ErrNotProject) {
			t.Errorf("Root(%s) = %v, want ErrNotProject", dir, err)
		}
	}
}
