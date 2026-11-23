package watch

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/fsnotify"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

func newTrackerWithFiles(t *testing.T) (*tracker, *store.Store, string) {
	t.Helper()
	root := testutil.TempDir(t)
	for path, content := range map[string]string{
		"README.md":       "# demo",
		"src/main.go":     "package main",
		"src/lib/util.go": "package lib",
		"docs/guide.md":   "guide",
	} {
		testutil.WriteFile(t, filepath.Join(root, path), content)
	}
	return startTracker(t, root)
}

func startTracker(t *testing.T, root string) (*tracker, *store.Store, string) {
	t.Helper()
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker.Close)
	return tracker, projectStore, root
}

func damage(t *testing.T, root string) {
	t.Helper()
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# changed")
	os.RemoveAll(filepath.Join(root, "src"))
	testutil.WriteFile(t, filepath.Join(root, "new", "deep", "file.txt"), "new")
	os.Rename(filepath.Join(root, "docs", "guide.md"), filepath.Join(root, "docs", "moved.md"))
}

func takeCheckpoint(t *testing.T, tracker *tracker, label string) store.Checkpoint {
	t.Helper()
	checkpoint, err := tracker.Checkpoint(store.CheckpointOptions{Label: label, Dir: tracker.root})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func checkMatchesScan(t *testing.T, projectStore *store.Store, root string, id int) {
	t.Helper()
	matcher, _ := exclude.New(root)
	scanned, _, err := tree.Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := projectStore.ListingAt(id)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(scanned, stored) {
		changed, deleted := tree.Changed(stored, scanned)
		t.Fatalf("checkpoint %d differs from a scan: changed %v, deleted %v", id, changed, deleted)
	}
}

func checkpointThroughTracker(tracker *tracker) store.Checkpointer {
	return func(_ *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		return tracker.Checkpoint(options)
	}
}

func TestTrackerMatchesFullScan(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	damage(t, root)

	checkpoint := takeCheckpoint(t, tracker, "second")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerCheckpointsRestoreExactly(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	before := testutil.Fingerprint(t, root)
	takeCheckpoint(t, tracker, "first")
	takeCheckpoint(t, tracker, "second")
	damage(t, root)
	takeCheckpoint(t, tracker, "third")

	if _, err := restore.Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatalf("restore through tracker layers differs:\nbefore %v\nafter  %v", before, after)
	}
}

func TestTrackerSeesChangeRightBeforeCheckpoint(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	takeCheckpoint(t, tracker, "warm")

	for i := range 20 {
		name := fmt.Sprintf("f%d.txt", i)
		testutil.WriteFile(t, filepath.Join(root, name), "x")
		checkpoint := takeCheckpoint(t, tracker, "after write")
		manifest, _ := projectStore.Manifest(checkpoint.ID)
		if _, ok := manifest.Changed[name]; !ok {
			t.Fatalf("round %d: checkpoint missed %s written just before it", i, name)
		}
	}
}

func TestTrackerRecoversAfterOutsideCheckpoint(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	before := testutil.Fingerprint(t, root)
	takeCheckpoint(t, tracker, "first")
	takeCheckpoint(t, tracker, "second")

	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# outside")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "taken without the tracker", Dir: root}); err != nil {
		t.Fatal(err)
	}
	damage(t, root)
	takeCheckpoint(t, tracker, "after outside")

	if _, err := restore.Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatal("restore after an outside checkpoint is not exact")
	}
}

func TestTrackerFollowsMovedDirectories(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")

	os.Rename(filepath.Join(root, "src"), filepath.Join(root, "moved"))
	takeCheckpoint(t, tracker, "after move")
	testutil.WriteFile(t, filepath.Join(root, "moved", "lib", "util.go"), "package lib // edited")
	testutil.WriteFile(t, filepath.Join(root, "moved", "lib", "new.go"), "package lib")

	checkpoint := takeCheckpoint(t, tracker, "after edit")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesRecreatedProject(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")

	os.RemoveAll(root)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# recreated")
	takeCheckpoint(t, tracker, "after recreate")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited again")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main")

	checkpoint := takeCheckpoint(t, tracker, "after edit")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerFollowsExcludeRuleChanges(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "data\n")
	testutil.WriteFile(t, filepath.Join(root, "data", "important.db"), "precious")
	takeCheckpoint(t, tracker, "data excluded")

	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "# protect data now\n")
	checkpoint := takeCheckpoint(t, tracker, "data included")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)

	os.Remove(filepath.Join(root, "README.md"))
	if _, err := restore.Apply(projectStore, checkpoint.ID, checkpointThroughTracker(tracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "data", "important.db")); string(data) != "precious" {
		t.Fatalf("data/important.db = %q after the restore", data)
	}
}

func TestTrackerAppliesDirectoryPatterns(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "build/\n")
	takeCheckpoint(t, tracker, "first")

	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "old build")
	checkpoint := takeCheckpoint(t, tracker, "second")
	state, _ := projectStore.State(checkpoint.ID)
	if _, ok := state["build/out.bin"]; ok {
		t.Fatal("the tracker recorded a path that build/ excludes")
	}

	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "new build")
	os.Remove(filepath.Join(root, "README.md"))
	if _, err := restore.Apply(projectStore, checkpoint.ID, checkpointThroughTracker(tracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "build", "out.bin")); string(data) != "new build" {
		t.Fatalf("restore changed an excluded file: build/out.bin = %q", data)
	}
}

func TestTrackerWithRootTypedInOtherCase(t *testing.T) {
	parent := testutil.TempDir(t)
	stored := filepath.Join(parent, "MyProj")
	testutil.WriteFile(t, filepath.Join(stored, "a.txt"), "v1")
	typed := filepath.Join(parent, strings.ToLower("MyProj"))
	if _, err := os.Stat(typed); err != nil {
		t.Skip("the filesystem is case-sensitive")
	}
	root, err := project.Root(typed)
	if err != nil {
		t.Fatal(err)
	}
	tracker, projectStore, _ := startTracker(t, root)
	takeCheckpoint(t, tracker, "first")

	testutil.WriteFile(t, filepath.Join(stored, "a.txt"), "v2 edited")
	checkpoint := takeCheckpoint(t, tracker, "second")

	checkMatchesScan(t, projectStore, stored, checkpoint.ID)
}

func TestTrackerRecordsDirectoryThatBecomesReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	tracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "data", "rows"), "data")
	os.Chmod(filepath.Join(root, "data"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "data"), 0o755) })
	takeCheckpoint(t, tracker, "unreadable")

	os.Chmod(filepath.Join(root, "data"), 0o755)
	checkpoint := takeCheckpoint(t, tracker, "readable")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerFollowsHardLinks(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	os.Link(filepath.Join(root, "README.md"), filepath.Join(root, "docs", "README.link"))
	takeCheckpoint(t, tracker, "first")

	testutil.WriteFile(t, filepath.Join(root, "docs", "README.link"), "# written through the link")
	checkpoint := takeCheckpoint(t, tracker, "second")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSavesListingWhenClosed(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited")
	checkpoint := takeCheckpoint(t, tracker, "second")

	tracker.Close()

	data, err := os.ReadFile(filepath.Join(projectStore.Dir(), "listing.gob"))
	if err != nil || len(data) == 0 {
		t.Fatalf("no listing saved: %v", err)
	}
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerRescansAfterDroppedEvents(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "missed.txt"), "created while events were dropped")
	takeCheckpoint(t, tracker, "second")
	delete(tracker.listing, "missed.txt")

	tracker.handleEvents([]fsnotify.Event{{Path: root, ShouldRescan: true}})
	checkpoint := takeCheckpoint(t, tracker, "after dropped events")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesDirectoryReplacedByAnother(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "staging", "new.md"), "fresh docs")
	os.RemoveAll(filepath.Join(root, "docs"))
	if err := os.Rename(filepath.Join(root, "staging"), filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}

	checkpoint := takeCheckpoint(t, tracker, "after swap")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesRenamesThatOnlyChangeCase(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.md"), "v1")
	takeCheckpoint(t, tracker, "first")
	os.Rename(filepath.Join(root, "notes.md"), filepath.Join(root, "Notes.md"))
	os.Rename(filepath.Join(root, "src"), filepath.Join(root, "Src"))
	checkpoint := takeCheckpoint(t, tracker, "renamed")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)

	testutil.WriteFile(t, filepath.Join(root, "Notes.md"), "v3")
	edited := takeCheckpoint(t, tracker, "edited")
	os.Remove(filepath.Join(root, "Notes.md"))
	os.RemoveAll(filepath.Join(root, "Src"))
	takeCheckpoint(t, tracker, "deleted")
	if _, err := restore.Apply(projectStore, edited.ID, checkpointThroughTracker(tracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "Notes.md")); string(data) != "v3" {
		t.Fatalf("Notes.md = %q after restore, want v3", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "Src", "lib", "util.go")); string(data) != "package lib" {
		t.Fatalf("Src/lib/util.go = %q after restore", data)
	}
}

func TestTrackerSeesRenamesThatOnlyChangeUnicodeForm(t *testing.T) {
	tracker, projectStore, root := newTrackerWithFiles(t)
	composed, decomposed := "café.txt", "café.txt"
	testutil.WriteFile(t, filepath.Join(root, composed), "v1")
	takeCheckpoint(t, tracker, "first")
	os.Rename(filepath.Join(root, composed), filepath.Join(root, decomposed))

	checkpoint := takeCheckpoint(t, tracker, "renamed")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerResumesWhereThePreviousOneStopped(t *testing.T) {
	if !fsnotify.CanResume {
		t.Skip("this system keeps no event history")
	}
	tracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, tracker, "first")
	tracker.Close()
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited while no watcher ran")
	os.RemoveAll(filepath.Join(root, "src"))
	testutil.WriteFile(t, filepath.Join(root, "new", "file.txt"), "new")

	resumed, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	if resumed.shouldRescan {
		t.Fatal("a tracker started after a clean stop rescans the whole project")
	}
	checkpoint := takeCheckpoint(t, resumed, "second")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerKeepsUnreadableDirectoriesUntilReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any directory")
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	locked := filepath.Join(root, "locked")
	testutil.WriteFile(t, filepath.Join(locked, "secret.txt"), "secret")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited")

	edited := takeCheckpoint(t, projectTracker, "edited")
	manifest, err := projectStore.Manifest(edited.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifest.Unreadable, "locked") {
		t.Fatalf("checkpoint %d forgot that locked cannot be read: %v", edited.ID, manifest.Unreadable)
	}

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	readable := takeCheckpoint(t, projectTracker, "readable")
	if manifest, err = projectStore.Manifest(readable.ID); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(manifest.Unreadable, "locked") {
		t.Fatalf("checkpoint %d still calls locked unreadable after chmod 755", readable.ID)
	}
	checkMatchesScan(t, projectStore, root, readable.ID)
}

func TestTrackerNeverActsThroughSymlinkedParent(t *testing.T) {
	for name, create := range map[string]string{"file": "a/secret", "directory": "a/home/deep"} {
		t.Run(name, func(t *testing.T) {
			projectTracker, _, root := newTrackerWithFiles(t)
			outside := testutil.TempDir(t)
			testutil.WriteFile(t, filepath.Join(outside, "secret"), "do not delete")
			testutil.WriteFile(t, filepath.Join(outside, "home", "deep", "file"), "do not delete")
			takeCheckpoint(t, projectTracker, "first")

			if err := os.MkdirAll(filepath.Join(root, filepath.Dir(create)), 0o755); err != nil {
				t.Fatal(err)
			}
			testutil.WriteFile(t, filepath.Join(root, create), "")
			if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a2")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
				t.Fatal(err)
			}

			takeCheckpoint(t, projectTracker, "swapped")
			for _, rel := range []string{"secret", "home/deep/file"} {
				if data, err := os.ReadFile(filepath.Join(outside, rel)); err != nil || string(data) != "do not delete" {
					t.Errorf("%s outside the project = %q, %v", rel, data, err)
				}
			}
		})
	}
}
