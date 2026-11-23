package watch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

// On Linux, inotify does not watch excluded directories, so a new name made
// in one is seen only at the next full comparison, as design.md says.

func TestTrackerSeesNewNameInExcludedFolder(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "keep"), "dependency")
	if err := os.Link(filepath.Join(root, "README.md"), filepath.Join(root, "node_modules", "readme-link")); err != nil {
		t.Fatal(err)
	}

	checkpoint := takeCheckpoint(t, projectTracker, "second")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}
