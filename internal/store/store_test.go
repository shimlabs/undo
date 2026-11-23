package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"

	"github.com/shimlabs/undo/internal/project"
)

func TestCreateMakesStoreUnderHome(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()

	projectStore, err := Create(home, root)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, project.ID(root))
	if projectStore.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", projectStore.Dir(), want)
	}
	info, err := os.Stat(filepath.Join(want, "layers"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("layers is not a directory")
	}
}

func TestOpenNeverCreates(t *testing.T) {
	home := t.TempDir()

	if _, err := Open(home, t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open = %v, want an error matching fs.ErrNotExist", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("Open created %v", entries)
	}
}

func TestCreatePutsStoreNextToProjectWhenHomeIsInside(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())

	projectStore, err := Create(filepath.Join(root, "Library", "undo"), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(root), ".undo-"+project.ID(root)); projectStore.Dir() != want {
		t.Fatalf("store = %s, want %s", projectStore.Dir(), want)
	}
}

func TestOpenFindsStoreNextToDeletedProject(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(parent, "app")
	os.Mkdir(root, 0o755)
	home := filepath.Join(root, "Library", "undo")
	created, err := Create(home, root)
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(root)

	opened, err := Open(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Dir() != created.Dir() {
		t.Fatalf("Open found %s, want the store next to the project at %s", opened.Dir(), created.Dir())
	}
	stores, err := StoresUnder(home, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].Root() != root {
		t.Fatalf("StoresUnder = %v, want the store of %s", stores, root)
	}
}

func TestCreateRefusesHomeAndDiskRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{home, "/"} {
		if _, err := Create(t.TempDir(), root); !errors.Is(err, project.ErrNotProject) {
			t.Errorf("Create(%s) = %v, want ErrNotProject", root, err)
		}
	}
}

func TestOpenRefusesNewerFormat(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	projectStore, err := Create(home, root)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(projectStore.configPath(), config{Format: format + 1, Root: root})

	if _, err := Open(home, root); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Open = %v, want ErrNewerFormat", err)
	}
}

func TestStoresUnderFindsDeletedProjects(t *testing.T) {
	home, parent := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "app")
	os.Mkdir(root, 0o755)
	if _, err := Create(home, root); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(root)

	stores, err := StoresUnder(home, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].Root() != root {
		t.Fatalf("StoresUnder = %v, want the store of %s", stores, root)
	}
}

func TestCreateRefusesPlantedStoreNextToProject(t *testing.T) {
	for _, plant := range []string{"symlink", "shared directory"} {
		t.Run(plant, func(t *testing.T) {
			parent, _ := filepath.EvalSymlinks(t.TempDir())
			root := filepath.Join(parent, "proj")
			os.Mkdir(root, 0o755)
			planted := filepath.Join(parent, ".undo-"+project.ID(root))
			if plant == "symlink" {
				os.Symlink(t.TempDir(), planted)
			} else {
				os.Mkdir(planted, 0o777)
				os.Chmod(planted, 0o777)
			}

			if projectStore, err := Create(filepath.Join(root, "Library", "undo"), root); err == nil {
				t.Fatalf("Create adopted a %s at %s", plant, projectStore.Dir())
			}
		})
	}
}

func TestCreateRefusesSymlinkedHome(t *testing.T) {
	realHome, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(realHome, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)

	if _, err := Create(t.TempDir(), realHome); !errors.Is(err, project.ErrNotProject) {
		t.Fatalf("Create(home through a symlink) = %v, want ErrNotProject", err)
	}
}

func TestStoredPathsCannotLeaveProject(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	testutil.WriteFile(t, filepath.Join(outside, "precious"), "data")
	projectStore.Take(CheckpointOptions{Label: "first", Dir: root})
	escape, _ := filepath.Rel(projectStore.baseDir(), outside)
	writeJSON(filepath.Join(projectStore.layerDir(1), "manifest.json"), Manifest{Deleted: []string{escape}})

	if _, err := projectStore.State(1); err == nil {
		t.Fatal("State accepted a stored path outside the project")
	}
	writeJSON(projectStore.mergePath(), pendingMerge{Through: 1})
	projectStore.Take(CheckpointOptions{Label: "second", Dir: root})
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("a planted merge deleted a file outside the store: %v", err)
	}
}
