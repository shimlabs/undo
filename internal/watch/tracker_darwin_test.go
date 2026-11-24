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

func TestWriteThroughOutsideLinkMadeWhileWatching(t *testing.T) {
	outsideDir := testutil.TempDir(t)
	outside := filepath.Join(outsideDir, "shared.txt")
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	inside := filepath.Join(root, "shared.txt")
	testutil.WriteFile(t, inside, "v1")
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Link(inside, outside); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "3")
	// Writing in place keeps the inode, so the project's name sees the write.
	if err := os.WriteFile(outside, []byte("v2 important"), 0o644); err != nil {
		t.Fatal(err)
	}
	fourth := takeCheckpoint(t, projectTracker, "4")
	listing, err := projectStore.ListingAt(fourth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if listing["shared.txt"].Size != int64(len("v2 important")) {
		t.Fatalf("checkpoint %d missed the write through the outside name: %+v", fourth.ID, listing["shared.txt"])
	}
}

// FSEvents can deliver a change made before the marker after it, when it
// joins a later event for the same path. A checkpoint must not leave out what
// such an event would have reported.
func TestTrackerSeesChangesWhoseEventsComeLate(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "É", "É"), "made before the checkpoint")
	if err := os.RemoveAll(filepath.Join(root, "src", "lib")); err != nil {
		t.Fatal(err)
	}
	if !projectTracker.waitForMarker() {
		t.Fatal("the events never arrived")
	}
	projectTracker.mu.Lock()
	clear(projectTracker.dirty)
	projectTracker.mu.Unlock()

	checkpoint := takeCheckpoint(t, projectTracker, "second")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

// dropEvents forgets the events for paths, as if FSEvents had not delivered
// them yet.
func dropEvents(projectTracker *tracker, paths ...string) {
	projectTracker.mu.Lock()
	defer projectTracker.mu.Unlock()
	for _, fullPath := range paths {
		delete(projectTracker.dirty, fullPath)
	}
}

// A folder whose event came on time is read again by the events applied, so
// a second change in it whose event comes late must still be found.
func TestTrackerSeesLateChangeInFolderWithEventOnTime(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "src", "on-time.go"), "package main")
	testutil.WriteFile(t, filepath.Join(root, "src", "late.go"), "package main")
	if !projectTracker.waitForMarker() {
		t.Fatal("the events never arrived")
	}
	dropEvents(projectTracker, filepath.Join(root, "src", "late.go"))

	checkpoint := takeCheckpoint(t, projectTracker, "second")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

// A late event costs a read of its folder, not a comparison of the whole
// project: a write into an existing file elsewhere, whose event is also late,
// waits for the next checkpoint, as design.md says.
func TestTrackerReadsOnlyTheFolderOfALateChange(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "docs", "guide.md"), "edited in place")
	testutil.WriteFile(t, filepath.Join(root, "src", "late.go"), "package main")
	if !projectTracker.waitForMarker() {
		t.Fatal("the events never arrived")
	}
	projectTracker.mu.Lock()
	clear(projectTracker.dirty)
	projectTracker.mu.Unlock()

	checkpoint := takeCheckpoint(t, projectTracker, "second")
	listing, err := projectStore.ListingAt(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listing["src/late.go"]; !ok {
		t.Error("the checkpoint lacks src/late.go")
	}
	if listing["docs/guide.md"].Size == int64(len("edited in place")) {
		t.Error("a late change in src made the checkpoint compare the whole project")
	}
}

// A rename reports the old name and the new one, and the new name's event
// can be the one that comes late; the renamed tree must still be in the
// checkpoint.
func TestTrackerSeesRenameWhoseNewNameComesLate(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "source")); err != nil {
		t.Fatal(err)
	}
	if !projectTracker.waitForMarker() {
		t.Fatal("the events never arrived")
	}
	dropEvents(projectTracker, filepath.Join(root, "source"))

	checkpoint := takeCheckpoint(t, projectTracker, "second")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}
