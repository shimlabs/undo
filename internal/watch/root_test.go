package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestProjectRootDeletedAndRecreated(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	second := takeCheckpoint(t, projectTracker, "2")
	checkMatchesScan(t, projectStore, root, second.ID)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "recloned")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main // v2")
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
	time.Sleep(200 * time.Millisecond)
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main // v3 edited after reclone")
	fourth := takeCheckpoint(t, projectTracker, "4")
	checkMatchesScan(t, projectStore, root, fourth.ID)
}

func TestProjectRootMovedAwayAndReplaced(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	staging := root + ".new"
	testutil.WriteFile(t, filepath.Join(staging, "README.md"), "fresh")
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root + ".old") })
	if err := os.Rename(staging, root); err != nil {
		t.Fatal(err)
	}
	second := takeCheckpoint(t, projectTracker, "2")
	checkMatchesScan(t, projectStore, root, second.ID)
	time.Sleep(200 * time.Millisecond)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "edited after the swap")
	testutil.WriteFile(t, filepath.Join(root+".old", "README.md"), "old tree edited")
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
}
