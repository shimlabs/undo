package cli

import (
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
