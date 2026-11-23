package store_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
)

func TestCopyModeRestoresExactly(t *testing.T) {
	s, root := newProject(t)
	if err := s.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "src", "main.go"), []byte("package main"))
	want := fingerprint(root)
	if _, err := s.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := restore.Apply(s, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(root); !maps.Equal(got, want) {
		t.Fatalf("after restore:\n got %v\nwant %v", got, want)
	}
	reopened, err := store.Open(filepath.Dir(s.Dir()), root)
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
	writeFile(t, filepath.Join(root, "main.go"), []byte("package main"))
	s, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestTakeRefusesWithoutClones(t *testing.T) {
	s, _ := plainProject(t)

	_, err := s.Take(store.CheckpointOptions{Label: "first", Dir: s.Root()})
	if !errors.Is(err, store.ErrCloneUnsupported) {
		t.Fatalf("Take = %v, want ErrCloneUnsupported", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("checkpoints = %v after refusing", list)
	}
}

func TestCopyModeWorksWithoutClones(t *testing.T) {
	s, root := plainProject(t)
	if err := s.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	want := fingerprint(root)
	if _, err := s.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "main.go"))
	if _, err := restore.Apply(s, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(root); !maps.Equal(got, want) {
		t.Fatalf("after restore:\n got %v\nwant %v", got, want)
	}
}

func TestTakeRefusesWhenFilesAppearLater(t *testing.T) {
	s, root := plainProject(t)
	os.Remove(filepath.Join(root, "main.go"))
	if _, err := s.Take(store.CheckpointOptions{Label: "empty", Dir: root}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, "main.go"), []byte("package main"))
	_, err := s.Take(store.CheckpointOptions{Label: "first file", Dir: root})
	if !errors.Is(err, store.ErrCloneUnsupported) {
		t.Fatalf("Take = %v, want ErrCloneUnsupported", err)
	}
}
