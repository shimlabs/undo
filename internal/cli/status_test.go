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

// plainProject returns a project on a filesystem without clones.
func plainProject(t *testing.T) string {
	t.Helper()
	root := testutil.PlainDir(t)
	t.Setenv("UNDO_HOME", t.TempDir())
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
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

func TestUnreadableIgnoreFileSaysCannotOpen(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "dist/\n")
	if err := os.Chmod(filepath.Join(root, ".undoignore"), 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, ".undoignore"), 0o644)

	code, out := run(t, root, "", "checkpoint")
	if code != 1 || !strings.Contains(out, "Error: Cannot open .undoignore: permission denied.\nRun chmod u+r .undoignore in the project folder, then try again.\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
