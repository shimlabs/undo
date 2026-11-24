package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

func TestStatusHidesErrorsOlderThanLatestCheckpoint(t *testing.T) {
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	projectStore, err := store.Create(defaultStoreHome(t), root)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(projectStore.Dir(), "errors.log"), "2020-01-02T03:04:05Z disk full\n")
	mustRun(t, root, "checkpoint")

	if out := mustRun(t, root, "status"); strings.Contains(out, "Last error") {
		t.Fatalf("status shows an error from before the latest checkpoint:\n%s", out)
	}
}

// plainProject returns a project on the filesystem without clones that
// UNDO_TEST_PLAIN_DIR names.
func plainProject(t *testing.T) string {
	t.Helper()
	plain := os.Getenv("UNDO_TEST_PLAIN_DIR")
	if plain == "" {
		t.Skip("UNDO_TEST_PLAIN_DIR is not set")
	}
	t.Setenv("UNDO_HOME", t.TempDir())
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	root, err := os.MkdirTemp(plain, "project-") //nolint:usetesting // the project must be on the filesystem UNDO_TEST_PLAIN_DIR names
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	return root
}

func TestStatusAfterRefusedCheckpointPointsToCopyMode(t *testing.T) {
	root := plainProject(t)
	if code, out := run(t, root, "", "checkpoint"); code != 1 {
		t.Fatalf("checkpoint without clones: exit %d:\n%s", code, out)
	}

	out := mustRun(t, root, "status")
	if !strings.Contains(out, "Checkpoints  none yet; this filesystem cannot clone, so run undo checkpoint --copy") || strings.Contains(out, "Storage") {
		t.Fatalf("status after a refused checkpoint:\n%s", out)
	}
	if out := mustRun(t, root, "list"); !strings.Contains(out, "Run undo checkpoint --copy to start; this filesystem cannot clone.") {
		t.Fatalf("list after a refused checkpoint:\n%s", out)
	}
}
