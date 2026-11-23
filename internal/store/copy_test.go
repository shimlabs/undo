package store_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

func TestCopyModeRestoresExactly(t *testing.T) {
	projectStore, root := newEmptyProject(t)
	if err := projectStore.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main")
	want := testutil.Fingerprint(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

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
	root, err := os.MkdirTemp(plain, "project-") //nolint:usetesting // the project must be on the filesystem UNDO_TEST_PLAIN_DIR names
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
	if checkpoints, _ := projectStore.List(); len(checkpoints) != 0 {
		t.Fatalf("checkpoints = %v after refusing", checkpoints)
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
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := restore.Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Fingerprint(t, root); !maps.Equal(got, want) {
		t.Fatalf("after restore:\n got %v\nwant %v", got, want)
	}
}

func TestTakeRefusesWhenFilesAppearLater(t *testing.T) {
	projectStore, root := plainProject(t)
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "empty", Dir: root}); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	_, err := projectStore.Take(store.CheckpointOptions{Label: "first file", Dir: root})
	if !errors.Is(err, store.ErrCloneUnsupported) {
		t.Fatalf("Take = %v, want ErrCloneUnsupported", err)
	}
}

func TestCheckpointSeesCopyModeSetByAnotherProcess(t *testing.T) {
	root := testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a")
	home := t.TempDir()
	watcherStore, err := store.Create(home, root)
	if err != nil {
		t.Fatal(err)
	}
	defer watcherStore.Close()
	commandStore, err := store.Open(home, root)
	if err != nil {
		t.Fatal(err)
	}
	defer commandStore.Close()

	if err := commandStore.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	if _, err := watcherStore.Take(store.CheckpointOptions{Label: "after --copy", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if !watcherStore.IsCopyMode() {
		t.Fatal("a store opened earlier still takes clones after copy mode was set")
	}
}
