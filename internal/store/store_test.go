package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/testutil"
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
	info, err := os.Stat(projectStore.layersDir())
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
	root := testutil.TempDir(t)

	projectStore, err := Create(filepath.Join(root, "Library", "undo"), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(root), storeDirPrefix+project.ID(root)); projectStore.Dir() != want {
		t.Fatalf("store = %s, want %s", projectStore.Dir(), want)
	}
}

func TestOpenFindsStoreNextToDeletedProject(t *testing.T) {
	parent := testutil.TempDir(t)
	root := filepath.Join(parent, "app")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := Create(filepath.Join(root, "Library", "undo"), root)
	if err != nil {
		t.Fatal(err)
	}
	// A project on another volume than home keeps its store next to it and
	// its record in home, which survives deleting the project.
	home := t.TempDir()
	if err := recordLocation(home, created.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

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
	if err := writeJSON(projectStore.configPath(), config{Format: format + 1, Root: root}); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(home, root); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Open = %v, want ErrNewerFormat", err)
	}
}

func TestStoresUnderFindsDeletedProjects(t *testing.T) {
	home, parent := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "app")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(home, root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

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
			parent := testutil.TempDir(t)
			root := filepath.Join(parent, "proj")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			planted := filepath.Join(parent, storeDirPrefix+project.ID(root))
			if plant == "symlink" {
				if err := os.Symlink(t.TempDir(), planted); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(planted, 0o777); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(planted, 0o777); err != nil {
					t.Fatal(err)
				}
			}

			if projectStore, err := Create(filepath.Join(root, "Library", "undo"), root); err == nil {
				t.Fatalf("Create adopted a %s at %s", plant, projectStore.Dir())
			}
		})
	}
}

func TestCreateRefusesSymlinkedHome(t *testing.T) {
	realHome := testutil.TempDir(t)
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
	projectStore, _ := newProject(t, map[string]string{"a.txt": "a"})
	outside := testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(outside, "precious"), "data")
	take(t, projectStore, "first")
	escape, err := filepath.Rel(projectStore.baseDir(), outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(projectStore.manifestPath(1), Manifest{Deleted: []string{escape}}); err != nil {
		t.Fatal(err)
	}

	if _, err := projectStore.State(1); err == nil {
		t.Fatal("State accepted a stored path outside the project")
	}
	if err := writeJSON(projectStore.mergePath(), pendingMerge{Through: 1}); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "second")
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("a planted merge deleted a file outside the store: %v", err)
	}
}

func TestOpenIgnoresStoreNextToProjectThatHomeDidNotCreate(t *testing.T) {
	parent := testutil.TempDir(t)
	root := filepath.Join(parent, "proj")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A private store with valid settings, unpacked from an archive or
	// written by an agent, is still not one this user's undo created.
	planted := filepath.Join(parent, storeDirPrefix+project.ID(root))
	if err := os.Mkdir(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(planted, configFile), config{Format: format, Root: root}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	if _, err := Open(home, root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open = %v, want no store", err)
	}
	projectStore, err := Create(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if projectStore.Dir() == planted {
		t.Fatal("Create adopted a store it did not create")
	}
	stores, err := StoresUnder(home, parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, found := range stores {
		if found.Dir() == planted {
			t.Fatal("StoresUnder listed a store it did not create")
		}
	}
}

func TestOpenRefusesStoreWithSymlinkedBase(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	take(t, projectStore, "first")
	if err := os.RemoveAll(projectStore.baseDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), projectStore.baseDir()); err != nil {
		t.Fatal(err)
	}

	home := filepath.Dir(projectStore.Dir())
	if _, err := Open(home, root); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("Open = %v, want ErrUnsafeStore", err)
	}
}

func TestStoreNeverWritesThroughSymlinkedFiles(t *testing.T) {
	for _, name := range []string{logFile, pruneMarkerFile, indexFile, lockFile} {
		t.Run(name, func(t *testing.T) {
			projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
			take(t, projectStore, "first")
			outside := filepath.Join(t.TempDir(), "id_ed25519")
			testutil.WriteFile(t, outside, "secret key")
			target := filepath.Join(projectStore.Dir(), name)
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}

			testutil.WriteFile(t, filepath.Join(root, "a.txt"), "changed")
			projectStore.LogError(errors.New("logged"))
			// A planted symlink can make these fail; only the file outside
			// the store matters.
			_, _ = projectStore.Take(CheckpointOptions{Label: "second", Dir: root})
			_, _ = projectStore.PruneIfDue(time.Now().Add(2*time.Hour), DefaultLimits())

			if data, err := os.ReadFile(outside); err != nil || string(data) != "secret key" {
				t.Fatalf("file outside the store = %q, %v", data, err)
			}
		})
	}
}
