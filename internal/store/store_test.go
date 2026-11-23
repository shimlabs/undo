package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
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
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("Open created %v (%v)", entries, err)
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
	if err := projectStore.writeJSON(projectStore.configPath(), config{Format: format + 1, Root: root}); err != nil {
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
	if err := projectStore.writeJSON(projectStore.manifestPath(1), Manifest{Deleted: []string{escape}}); err != nil {
		t.Fatal(err)
	}

	if _, err := projectStore.State(1); err == nil {
		t.Fatal("State accepted a stored path outside the project")
	}
	if err := projectStore.writeJSON(projectStore.mergePath(), pendingMerge{Through: 1}); err != nil {
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
	if err := writeJSONFile(filepath.Join(planted, configFile), config{Format: format, Root: root}); err != nil {
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

// writeJSONFile writes value as JSON to path, for a folder that is no open
// store.
func writeJSONFile(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func TestOpenStoreNeverDeletesThroughSwappedBase(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"secret": "s", "docs/guide.md": "g"})
	take(t, projectStore, "first")
	outside := testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(outside, "secret"), "do not delete")
	testutil.WriteFile(t, filepath.Join(outside, "docs", "guide.md"), "do not delete")
	// The store is already open, as the watcher keeps it, when base is
	// swapped and a merge that deletes from it is planted.
	if err := os.Rename(projectStore.baseDir(), projectStore.baseDir()+".aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, projectStore.baseDir()); err != nil {
		t.Fatal(err)
	}
	if err := projectStore.writeJSON(projectStore.manifestPath(1), Manifest{Deleted: []string{"secret"}}); err != nil {
		t.Fatal(err)
	}
	if err := projectStore.writeJSON(projectStore.mergePath(), pendingMerge{Through: 1}); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "n")
	_, _ = projectStore.Take(CheckpointOptions{Label: "second", Dir: root})
	for _, rel := range []string{"secret", "docs/guide.md"} {
		if data, err := os.ReadFile(filepath.Join(outside, rel)); err != nil || string(data) != "do not delete" {
			t.Errorf("%s outside the store = %q, %v", rel, data, err)
		}
	}
}

func TestExtractNeverReadsThroughSymlinkInLayer(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "changed")
	second := take(t, projectStore, "second")
	secrets := testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(secrets, "id_ed25519"), "private key")
	if err := os.Symlink(secrets, projectStore.layerFile(second.ID, "leak")); err != nil {
		t.Fatal(err)
	}

	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	_ = extractor.ExtractAt("leak/id_ed25519", Source{Layer: second.ID}, dir, "id_ed25519")
	if data, err := os.ReadFile(filepath.Join(root, "id_ed25519")); err == nil {
		t.Fatalf("a file from outside the store reached the project: %q", data)
	}
}

func TestStoresUnderClosesSkippedStores(t *testing.T) {
	home := t.TempDir()
	for i := range 50 {
		storeDir := filepath.Join(home, fmt.Sprintf("store%02d", i))
		if err := os.Mkdir(storeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONFile(filepath.Join(storeDir, configFile), config{Format: format, Root: "/elsewhere/project"}); err != nil {
			t.Fatal(err)
		}
	}
	before := openDescriptorCount(t)
	stores, err := StoresUnder(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 0 {
		t.Fatalf("StoresUnder found %d stores, want 0", len(stores))
	}
	if after := openDescriptorCount(t); after > before {
		t.Fatalf("StoresUnder left %d descriptors open", after-before)
	}
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	count := 0
	for fd := range 4096 {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			count++
		}
	}
	return count
}

func TestCheckpointNeverReadsThroughProjectDirSwappedForSymlink(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"keys/id_ed25519": "dummy", "a.txt": "a"})
	first := take(t, projectStore, "first")
	outside := t.TempDir()
	testutil.WriteFile(t, filepath.Join(outside, "id_ed25519"), "private key")
	matcher, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err := tree.Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	// An agent's background process swaps the folder after the scan.
	if err := os.RemoveAll(filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := projectStore.Commit(first.ID, CheckpointOptions{Label: "swapped", Dir: root}, Changes{Listing: listing, Changed: []string{"keys/id_ed25519"}})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(projectStore.layerFile(checkpoint.ID, "keys/id_ed25519")); err == nil {
		t.Fatalf("checkpoint stored %q from outside the project", data)
	}
	manifest, err := projectStore.Manifest(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifest.Deleted, "keys/id_ed25519") {
		t.Fatalf("Deleted = %v, want keys/id_ed25519, which is no longer in the project", manifest.Deleted)
	}
}
