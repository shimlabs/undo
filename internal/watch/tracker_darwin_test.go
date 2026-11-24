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

// A rename onto a listed name changes its folder's times but none of its
// names, so the check must also see that the name holds another file.
func TestTrackerSeesLateRenameOntoListedName(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		late   []string
	}{
		{
			name: "save through a temporary file",
			change: func(t *testing.T, root string) {
				t.Helper()
				testutil.WriteFile(t, filepath.Join(root, "src", ".main.go.tmp"), "package main // saved")
				if err := os.Rename(filepath.Join(root, "src", ".main.go.tmp"), filepath.Join(root, "src", "main.go")); err != nil {
					t.Fatal(err)
				}
			},
			late: []string{"src/main.go"},
		},
		{
			name: "move over a listed file",
			change: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Rename(filepath.Join(root, "docs", "guide.md"), filepath.Join(root, "README.md")); err != nil {
					t.Fatal(err)
				}
			},
			late: []string{"README.md"},
		},
		{
			name: "replace a file by a folder",
			change: func(t *testing.T, root string) {
				t.Helper()
				testutil.WriteFile(t, filepath.Join(root, "src", "other.go"), "package main")
				if err := os.Remove(filepath.Join(root, "src", "main.go")); err != nil {
					t.Fatal(err)
				}
				testutil.WriteFile(t, filepath.Join(root, "src", "main.go", "inner.go"), "package inner")
			},
			late: []string{"src/main.go", "src/main.go/inner.go"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectTracker, projectStore, root := newTrackerWithFiles(t)
			takeCheckpoint(t, projectTracker, "first")
			test.change(t, root)
			if !projectTracker.waitForMarker() {
				t.Fatal("the events never arrived")
			}
			for _, rel := range test.late {
				dropEvents(projectTracker, filepath.Join(root, rel))
			}

			checkpoint := takeCheckpoint(t, projectTracker, "second")
			checkMatchesScan(t, projectStore, root, checkpoint.ID)
		})
	}
}
