package watch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/testutil"
)

const (
	composedName   = "café.txt"
	decomposedName = "café.txt"
)

func removeUnderOtherForm(t *testing.T, created, removed string, isSymlink bool) {
	t.Helper()
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	if isSymlink {
		if err := os.Symlink("README.md", filepath.Join(root, created)); err != nil {
			t.Fatal(err)
		}
	} else {
		testutil.WriteFile(t, filepath.Join(root, created), "v1")
	}
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Remove(filepath.Join(root, removed)); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "after removal")
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
}

func TestRemoveNFCFileByNFDName(t *testing.T) {
	removeUnderOtherForm(t, composedName, decomposedName, false)
}
func TestRemoveNFDFileByNFCName(t *testing.T) {
	removeUnderOtherForm(t, decomposedName, composedName, false)
}
func TestRemoveNFCLinkByNFDName(t *testing.T) {
	removeUnderOtherForm(t, composedName, decomposedName, true)
}
func TestRemoveNFDLinkByNFCName(t *testing.T) {
	removeUnderOtherForm(t, decomposedName, composedName, true)
}

func TestRemoveUnderOtherFormThenRestore(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, composedName), "v1")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Remove(filepath.Join(root, decomposedName)); err != nil {
		t.Fatal(err)
	}
	third := takeCheckpoint(t, projectTracker, "3")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "later")
	takeCheckpoint(t, projectTracker, "4")
	if _, err := restore.Apply(projectStore, third.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, composedName)); err == nil {
		t.Fatalf("restoring checkpoint %d, taken after %s was deleted, brought it back", third.ID, composedName)
	}
}

func TestRemoveByOtherFormAfterSubdirEvents(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	if err := os.Symlink("c", filepath.Join(root, "é")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "2")
	if err := os.MkdirAll(filepath.Join(root, "a", "é", "é"), 0o755); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "3")
	if err := os.Remove(filepath.Join(root, "é")); err != nil {
		t.Fatal(err)
	}
	fourth := takeCheckpoint(t, projectTracker, "4")
	checkMatchesScan(t, projectStore, root, fourth.ID)
}

func TestRemoveByOtherFormThenRestoreKeepsItDeleted(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "café.txt"), "draft")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.MkdirAll(filepath.Join(root, "a", "café.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "3")
	if err := os.Remove(filepath.Join(root, "café.txt")); err != nil {
		t.Fatal(err)
	}
	fourth := takeCheckpoint(t, projectTracker, "4")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "later")
	takeCheckpoint(t, projectTracker, "5")
	if _, err := restore.Apply(projectStore, fourth.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "café.txt")); err == nil {
		t.Fatalf("restoring checkpoint %d, taken after café.txt was deleted, brought it back", fourth.ID)
	}
}

func TestRemoveByOtherFormAfterIdleCheckpoint(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "café.txt"), "draft")
	takeCheckpoint(t, projectTracker, "2")
	takeCheckpoint(t, projectTracker, "3")
	if err := os.Remove(filepath.Join(root, "café.txt")); err != nil {
		t.Fatal(err)
	}
	fourth := takeCheckpoint(t, projectTracker, "4")
	checkMatchesScan(t, projectStore, root, fourth.ID)
}

// The rename names the old path in a third spelling: neither the stored one
// nor the new one.
func renameThroughThirdSpelling(t *testing.T, stored, spelled, renamed string, isDir bool) {
	t.Helper()
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	if isDir {
		testutil.WriteFile(t, filepath.Join(root, stored, "a.go"), "v1")
	} else {
		testutil.WriteFile(t, filepath.Join(root, stored), "v1")
	}
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Rename(filepath.Join(root, spelled), filepath.Join(root, renamed)); err != nil {
		t.Fatal(err)
	}
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
}

func TestCaseRenameDirThroughThirdSpelling(t *testing.T) {
	renameThroughThirdSpelling(t, "lib", "LIB", "Lib", true)
}
func TestCaseRenameFileThroughThirdSpelling(t *testing.T) {
	renameThroughThirdSpelling(t, "readme.txt", "README.TXT", "Readme.txt", false)
}
func TestCaseRenameThroughThirdSpellingThenRestore(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "lib", "a.go"), "v1")
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Rename(filepath.Join(root, "LIB"), filepath.Join(root, "Lib")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "Lib", "b.go"), "new")
	third := takeCheckpoint(t, projectTracker, "3")
	want := testutil.Snapshot(t, root, testutil.SnapshotOptions{})
	if err := os.RemoveAll(filepath.Join(root, "Lib")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "4")
	changes, err := restore.Apply(projectStore, third.ID, checkpointThroughTracker(projectTracker))
	if err != nil {
		t.Fatal(err)
	}
	got := testutil.Snapshot(t, root, testutil.SnapshotOptions{})
	if diff := testutil.SnapshotDiff(want, got); len(diff) > 0 {
		for _, rel := range diff {
			t.Errorf("%+q: want %q, got %q", rel, want[rel], got[rel])
		}
		t.Logf("changes %+v", changes)
	}
}
func TestUnicodeRenameThroughThirdSpelling(t *testing.T) {
	renameThroughThirdSpelling(t, "café", "CAFÉ", "Café", true)
}

func TestCaseRenameThroughThirdSpellingAndEditThenRestore(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "lib", "a.go"), "v1")
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := os.Rename(filepath.Join(root, "LIB"), filepath.Join(root, "Lib")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "Lib", "a.go"), "v2 latest work")
	third := takeCheckpoint(t, projectTracker, "3")
	if err := os.RemoveAll(filepath.Join(root, "Lib")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "4")
	if _, err := restore.Apply(projectStore, third.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "Lib", "a.go"))
	if string(data) != "v2 latest work" {
		t.Fatalf("Lib/a.go = %q, %v after restoring checkpoint %d; want v2 latest work", data, err, third.ID)
	}
}

func TestSwapThroughOtherSpelling(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	dirWithFile("one")(t, filepath.Join(root, "lib"))
	dirWithFile("pkg")(t, filepath.Join(root, "node_modules"))
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := unix.RenamexNp(filepath.Join(root, "node_modules"), filepath.Join(root, "LIB"), unix.RENAME_SWAP); err != nil {
		t.Fatal(err)
	}
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
}

// A directory where a name the listing does not hold came and went is read
// before the checkpoint finishes. A named pipe in its place must not block
// that read.
func TestApplyWhenDirectoryBecameNamedPipe(t *testing.T) {
	projectTracker, _, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	dir := filepath.Join(root, "build")
	if err := unix.Mkfifo(dir, 0o644); err != nil {
		t.Fatal(err)
	}
	applied := make(chan error, 1)
	go func() {
		_, _, err := projectTracker.applyDirty(map[string]bool{filepath.Join(dir, "out.tmp"): false})
		applied <- err
	}()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("applying the events is stuck opening the named pipe")
	}
}

// The index of each folder's listed children is kept up to date from each
// checkpoint's changes, so it must stay what building it again gives.
func TestChildIndexFollowsListing(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "web", "app.js"), "v1")
	takeCheckpoint(t, projectTracker, "2")
	if projectTracker.children == nil {
		t.Fatal("a new name did not build the index")
	}
	if err := os.Rename(filepath.Join(root, "web"), filepath.Join(root, "Web")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "Web", "new.js"), "v1")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	checkMatchesScan(t, projectStore, root, takeCheckpoint(t, projectTracker, "3").ID)
	if want := newChildIndex(projectTracker.listing); !reflect.DeepEqual(projectTracker.children, want) {
		t.Fatalf("child index = %v, want %v", projectTracker.children, want)
	}
}

// Events spell a path the way the command did, so removing a folder by
// another case names a path the record of excluded paths does not hold.
// The root's own entry still changes.
func TestTrackerDropsExcludedFolderRemovedUnderOtherCase(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	takeCheckpoint(t, projectTracker, "first")
	if err := os.RemoveAll(filepath.Join(root, "NODE_MODULES")); err != nil {
		t.Fatal(err)
	}
	checkMatchesScan(t, projectStore, root, takeCheckpoint(t, projectTracker, "removed").ID)
}

// The default rule excludes node_modules by its exact name, so the folder
// renamed to another case is tracked and no longer excluded.
func TestTrackerDropsExcludedFolderRenamedToOtherCase(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	takeCheckpoint(t, projectTracker, "first")
	if err := os.Rename(filepath.Join(root, "node_modules"), filepath.Join(root, "Node_modules")); err != nil {
		t.Fatal(err)
	}
	checkMatchesScan(t, projectStore, root, takeCheckpoint(t, projectTracker, "renamed").ID)
}
