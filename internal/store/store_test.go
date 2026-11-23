package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/project"
)

func TestOpenCreatesStoreUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UNDO_HOME", home)
	root := t.TempDir()

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, project.ID(root))
	if s.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", s.Dir(), want)
	}
	info, err := os.Stat(filepath.Join(want, "layers"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("layers is not a directory")
	}
}

func TestLocatePutsStoreNextToProjectWhenHomeIsInside(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("UNDO_HOME", filepath.Join(root, "Library", "undo"))

	dir, err := Locate(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(root), ".undo-"+project.ID(root)); dir != want {
		t.Fatalf("Locate = %s, want %s", dir, want)
	}
}

func TestLocateRefusesHomeAndDiskRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{home, "/"} {
		if _, err := Locate(root); !errors.Is(err, ErrNotProject) {
			t.Errorf("Locate(%s) = %v, want ErrNotProject", root, err)
		}
	}
}

func TestOpenRefusesNewerFormat(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(s.configPath(), config{Format: format + 1, Root: root})

	if _, err := Open(root); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Open = %v, want ErrNewerFormat", err)
	}
}

func TestStoresFindsDeletedProjects(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "app")
	os.Mkdir(root, 0o755)
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(root)

	stores, err := Stores()
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].Root() != root {
		t.Fatalf("Stores = %v, want the store of %s", stores, root)
	}
}
