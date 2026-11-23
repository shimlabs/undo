package store_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
)

func TestCopyModeRestoresExactly(t *testing.T) {
	projectStore, root := newProject(t)
	if err := projectStore.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main")
	want := testutil.Fingerprint(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := restore.Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Fingerprint(t, root); !maps.Equal(got, want) {
		t.Fatalf("after restore:\n got %v\nwant %v", got, want)
	}
	reopened, err := store.Open(filepath.Dir(projectStore.Dir()), root)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsCopyMode() {
		t.Fatal("copy mode was not saved with the store")
	}
}

// plainProject returns a project directory on a filesystem without clones, set
// by UNDO_TEST_PLAIN_DIR. The Linux CI job points it at ext4 or overlayfs.
func plainProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	plain := os.Getenv("UNDO_TEST_PLAIN_DIR")
	if plain == "" {
		t.Skip("UNDO_TEST_PLAIN_DIR is not set")
	}
	root, err := os.MkdirTemp(plain, "project-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return projectStore, root
}

func TestTakeRefusesWithoutClones(t *testing.T) {
	projectStore, _ := plainProject(t)

	_, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: projectStore.Root()})
	if !errors.Is(err, store.ErrCloneUnsupported) {
		t.Fatalf("Take = %v, want ErrCloneUnsupported", err)
	}
	if list, _ := projectStore.List(); len(list) != 0 {
		t.Fatalf("checkpoints = %v after refusing", list)
	}
}

func TestCopyModeWorksWithoutClones(t *testing.T) {
	projectStore, root := plainProject(t)
	if err := projectStore.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	want := testutil.Fingerprint(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "main.go"))
	if _, err := restore.Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Fingerprint(t, root); !maps.Equal(got, want) {
		t.Fatalf("after restore:\n got %v\nwant %v", got, want)
	}
}

func TestTakeRefusesWhenFilesAppearLater(t *testing.T) {
	projectStore, root := plainProject(t)
	os.Remove(filepath.Join(root, "main.go"))
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "empty", Dir: root}); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	_, err := projectStore.Take(store.CheckpointOptions{Label: "first file", Dir: root})
	if !errors.Is(err, store.ErrCloneUnsupported) {
		t.Fatalf("Take = %v, want ErrCloneUnsupported", err)
	}
}
