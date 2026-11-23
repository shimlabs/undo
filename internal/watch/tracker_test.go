package watch

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/fsnotify"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
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

func startTracker(t testing.TB, root string) (*tracker, *store.Store, string) {
	t.Helper()
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	projectTracker, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(projectTracker.Close)
	return projectTracker, projectStore, root
}

func damage(t *testing.T, root string) {
	t.Helper()
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# changed")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "new", "deep", "file.txt"), "new")
	if err := os.Rename(filepath.Join(root, "docs", "guide.md"), filepath.Join(root, "docs", "moved.md")); err != nil {
		t.Fatal(err)
	}
}

func takeCheckpoint(t testing.TB, projectTracker *tracker, label string) store.Checkpoint {
	t.Helper()
	checkpoint, err := projectTracker.Checkpoint(store.CheckpointOptions{Label: label, Dir: projectTracker.root})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func checkMatchesScan(t *testing.T, projectStore *store.Store, root string, id int) {
	t.Helper()
	matcher, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}
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

func checkpointThroughTracker(projectTracker *tracker) store.Checkpointer {
	return func(_ *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		return projectTracker.Checkpoint(options)
	}
}

func TestTrackerMatchesFullScan(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	damage(t, root)

	checkpoint := takeCheckpoint(t, projectTracker, "second")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerCheckpointsRestoreExactly(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	before := testutil.Fingerprint(t, root)
	takeCheckpoint(t, projectTracker, "first")
	takeCheckpoint(t, projectTracker, "second")
	damage(t, root)
	takeCheckpoint(t, projectTracker, "third")

	if _, err := restore.Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatalf("restore through tracker layers differs:\nbefore %v\nafter  %v", before, after)
	}
}

func TestTrackerSeesChangeRightBeforeCheckpoint(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	takeCheckpoint(t, projectTracker, "warm")

	for i := range 20 {
		name := fmt.Sprintf("f%d.txt", i)
		testutil.WriteFile(t, filepath.Join(root, name), "x")
		checkpoint := takeCheckpoint(t, projectTracker, "after write")
		manifest, err := projectStore.Manifest(checkpoint.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := manifest.Changed[name]; !ok {
			t.Fatalf("round %d: checkpoint missed %s written just before it", i, name)
		}
	}
}

func TestTrackerRecoversAfterOutsideCheckpoint(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	before := testutil.Fingerprint(t, root)
	takeCheckpoint(t, projectTracker, "first")
	takeCheckpoint(t, projectTracker, "second")

	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# outside")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "taken without the tracker", Dir: root}); err != nil {
		t.Fatal(err)
	}
	damage(t, root)
	takeCheckpoint(t, projectTracker, "after outside")

	if _, err := restore.Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatal("restore after an outside checkpoint is not exact")
	}
}

func TestTrackerFollowsMovedDirectories(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")

	if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "after move")
	testutil.WriteFile(t, filepath.Join(root, "moved", "lib", "util.go"), "package lib // edited")
	testutil.WriteFile(t, filepath.Join(root, "moved", "lib", "new.go"), "package lib")

	checkpoint := takeCheckpoint(t, projectTracker, "after edit")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesRecreatedProject(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# recreated")
	takeCheckpoint(t, projectTracker, "after recreate")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited again")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main")

	checkpoint := takeCheckpoint(t, projectTracker, "after edit")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerFollowsExcludeRuleChanges(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "data\n")
	testutil.WriteFile(t, filepath.Join(root, "data", "important.db"), "precious")
	takeCheckpoint(t, projectTracker, "data excluded")

	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "# protect data now\n")
	checkpoint := takeCheckpoint(t, projectTracker, "data included")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)

	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := restore.Apply(projectStore, checkpoint.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "data", "important.db")); string(data) != "precious" {
		t.Fatalf("data/important.db = %q after the restore", data)
	}
}

func TestTrackerAppliesDirectoryPatterns(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "build/\n")
	takeCheckpoint(t, projectTracker, "first")

	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "old build")
	checkpoint := takeCheckpoint(t, projectTracker, "second")
	state, err := projectStore.State(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state["build/out.bin"]; ok {
		t.Fatal("the tracker recorded a path that build/ excludes")
	}

	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "new build")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := restore.Apply(projectStore, checkpoint.ID, checkpointThroughTracker(projectTracker)); err != nil {
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
	projectTracker, projectStore, _ := startTracker(t, root)
	takeCheckpoint(t, projectTracker, "first")

	testutil.WriteFile(t, filepath.Join(stored, "a.txt"), "v2 edited")
	checkpoint := takeCheckpoint(t, projectTracker, "second")

	checkMatchesScan(t, projectStore, stored, checkpoint.ID)
}

func TestTrackerRecordsDirectoryThatBecomesReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "data", "rows"), "data")
	if err := os.Chmod(filepath.Join(root, "data"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "data"), 0o755) })
	takeCheckpoint(t, projectTracker, "unreadable")

	if err := os.Chmod(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkpoint := takeCheckpoint(t, projectTracker, "readable")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerFollowsHardLinks(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	if err := os.Link(filepath.Join(root, "README.md"), filepath.Join(root, "docs", "README.link")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "first")

	testutil.WriteFile(t, filepath.Join(root, "docs", "README.link"), "# written through the link")
	checkpoint := takeCheckpoint(t, projectTracker, "second")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSavesListingWhenClosed(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited")
	checkpoint := takeCheckpoint(t, projectTracker, "second")

	projectTracker.Close()

	data, err := os.ReadFile(filepath.Join(projectStore.Dir(), "listing.gob"))
	if err != nil || len(data) == 0 {
		t.Fatalf("no listing saved: %v", err)
	}
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerRescansAfterDroppedEvents(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "missed.txt"), "created while events were dropped")
	takeCheckpoint(t, projectTracker, "second")
	delete(projectTracker.listing, "missed.txt")

	projectTracker.handleEvents([]fsnotify.Event{{Path: root, ShouldRescan: true}})
	checkpoint := takeCheckpoint(t, projectTracker, "after dropped events")

	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesDirectoryReplacedByAnother(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	testutil.WriteFile(t, filepath.Join(root, "staging", "new.md"), "fresh docs")
	if err := os.RemoveAll(filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "staging"), filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}

	checkpoint := takeCheckpoint(t, projectTracker, "after swap")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerSeesRenamesThatOnlyChangeCase(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.md"), "v1")
	takeCheckpoint(t, projectTracker, "first")
	if err := os.Rename(filepath.Join(root, "notes.md"), filepath.Join(root, "Notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "Src")); err != nil {
		t.Fatal(err)
	}
	checkpoint := takeCheckpoint(t, projectTracker, "renamed")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)

	testutil.WriteFile(t, filepath.Join(root, "Notes.md"), "v3")
	edited := takeCheckpoint(t, projectTracker, "edited")
	if err := os.Remove(filepath.Join(root, "Notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "Src")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "deleted")
	if _, err := restore.Apply(projectStore, edited.ID, checkpointThroughTracker(projectTracker)); err != nil {
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
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	composed, decomposed := "café.txt", "café.txt"
	testutil.WriteFile(t, filepath.Join(root, composed), "v1")
	takeCheckpoint(t, projectTracker, "first")
	if err := os.Rename(filepath.Join(root, composed), filepath.Join(root, decomposed)); err != nil {
		t.Fatal(err)
	}

	checkpoint := takeCheckpoint(t, projectTracker, "renamed")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestTrackerResumesWhereThePreviousOneStopped(t *testing.T) {
	if !fsnotify.CanResume {
		t.Skip("this system keeps no event history")
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "first")
	projectTracker.Close()
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# edited while no watcher ran")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
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

// BenchmarkTrackerCheckpoint measures a checkpoint after one file changed in
// a project of 10,000 files, the common case for an agent command.
func BenchmarkTrackerCheckpoint(b *testing.B) {
	root := testutil.TempDir(b)
	for i := range 10_000 {
		testutil.WriteFile(b, filepath.Join(root, fmt.Sprintf("dir%03d", i%200), fmt.Sprintf("file%05d.go", i)), "package main")
	}
	projectTracker, _, _ := startTracker(b, root)
	takeCheckpoint(b, projectTracker, "first")
	changed := filepath.Join(root, "dir000", "file00000.go")
	for i := 0; b.Loop(); i++ {
		testutil.WriteFile(b, changed, fmt.Sprint("package main // ", i))
		takeCheckpoint(b, projectTracker, "edit")
	}
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

// A file that was unreadable at checkpoint 2 and unchanged at checkpoint 3.
// Checkpoint 3 never saved it, so restoring 3 must not write an older copy.
func TestTrackerDoesNotRestoreOldContentOfUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	secret := filepath.Join(root, "secret.txt")
	testutil.WriteFile(t, secret, "v1")
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, secret, "v2")
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "2")
	third := takeCheckpoint(t, projectTracker, "3")
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "4")

	changes, err := restore.Apply(projectStore, third.ID, checkpointThroughTracker(projectTracker))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(secret); err == nil && string(data) != "v2" {
		t.Fatalf("restoring checkpoint 3 wrote secret.txt = %q, which it held only before checkpoint 2 (written %v)", data, changes.Written)
	}
}

// Same scenario through scans only, for comparison.
func TestScanDoesNotRestoreOldContentOfUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	_, projectStore, root := newTrackerWithFiles(t)
	secret := filepath.Join(root, "secret.txt")
	testutil.WriteFile(t, secret, "v1")
	take := func(label string) store.Checkpoint {
		checkpoint, err := projectStore.Take(store.CheckpointOptions{Label: label, Dir: root})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	take("1")
	testutil.WriteFile(t, secret, "v2")
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	take("2")
	third := take("3")
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	take("4")
	if _, err := restore.Apply(projectStore, third.ID, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(secret); err == nil && string(data) != "v2" {
		t.Fatalf("scan mode: secret.txt = %q", data)
	}
}

// A file edited inside a directory, then the directory renamed by case only,
// both before one checkpoint. The event for the old file path still stats.
func TestTrackerSeesEditThenCaseRenameOfParent(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "v2")
	if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "Src")); err != nil {
		t.Fatal(err)
	}
	renamed := takeCheckpoint(t, projectTracker, "2")
	checkMatchesScan(t, projectStore, root, renamed.ID)
}

func TestRestoreAfterEditThenCaseRenameOfParent(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "v2")
	if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "Src")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "2")
	testutil.WriteFile(t, filepath.Join(root, "Src", "main.go"), "v3")
	edited := takeCheckpoint(t, projectTracker, "3")
	if err := os.RemoveAll(filepath.Join(root, "Src")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "4")
	if _, err := restore.Apply(projectStore, edited.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "Src", "main.go")); string(data) != "v3" {
		t.Fatalf("Src/main.go = %q after restore, want v3", data)
	}
}

// The watcher is killed (no Close), scans take checkpoints, files change,
// then a new watcher resumes.
func TestTrackerResumesAfterKillAndScans(t *testing.T) {
	if !fsnotify.CanResume {
		t.Skip()
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "a")
	takeCheckpoint(t, projectTracker, "2")
	projectTracker.SaveState()
	projectTracker.stream.Close()
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "b")
	testutil.WriteFile(t, filepath.Join(root, "docs", "x.md"), "x")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "scan3", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "scan4", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "docs", "guide.md"), "back")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "changed after scans")

	resumed, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	if resumed.shouldRescan {
		t.Fatal("expected resume")
	}
	checkpoint := takeCheckpoint(t, resumed, "5")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

// A rules change made while no watcher ran.
func TestTrackerResumesAfterRulesChange(t *testing.T) {
	if !fsnotify.CanResume {
		t.Skip()
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	projectTracker.Close()
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "docs/\n")
	testutil.WriteFile(t, filepath.Join(root, "docs", "guide.md"), "edited")

	resumed, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	checkpoint := takeCheckpoint(t, resumed, "2")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

// A directory that was unreadable and becomes readable is restored correctly
// at the checkpoint where it became readable.
func TestRestoreOfDirectoryReadableAgain(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	locked := filepath.Join(root, "docs")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	projectTracker.mu.Lock()
	projectTracker.shouldRescan = true
	projectTracker.mu.Unlock()
	takeCheckpoint(t, projectTracker, "2 rescan")
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	readable := takeCheckpoint(t, projectTracker, "3 readable")
	testutil.WriteFile(t, filepath.Join(locked, "guide.md"), "damaged")
	takeCheckpoint(t, projectTracker, "4")
	manifest, err := projectStore.Manifest(readable.ID)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := restore.Apply(projectStore, readable.ID, checkpointThroughTracker(projectTracker))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(locked, "guide.md")); string(data) != "guide" {
		t.Fatalf("docs/guide.md = %q after restoring checkpoint %d (manifest unreadable %v, changes %+v)", data, readable.ID, manifest.Unreadable, changes)
	}
}

// An unreadable directory, then a watcher restart that resumes events.
// The resumed tracker must still know the directory was never saved.
func TestTrackerKeepsUnreadableDirectoryAcrossResume(t *testing.T) {
	if os.Getuid() == 0 || !fsnotify.CanResume {
		t.Skip()
	}
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	locked := filepath.Join(root, "docs")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	projectTracker.mu.Lock()
	projectTracker.shouldRescan = true
	projectTracker.mu.Unlock()
	takeCheckpoint(t, projectTracker, "2 rescan")
	projectTracker.Close()
	resumed, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "edit")
	third := takeCheckpoint(t, resumed, "3")
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, resumed, "4")
	changes, err := restore.Apply(projectStore, third.ID, checkpointThroughTracker(resumed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(locked, "guide.md")); err != nil {
		t.Fatalf("restoring checkpoint 3 removed docs/guide.md, which existed then but was unreadable (removed %v)", changes.Removed)
	}
}

// A directory moved out of the project, edited there, and moved back keeps
// its inode and mode, and the edit inside it has no event under the root.
func TestTrackerSeesDirectoryMovedOutEditedAndBack(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-aside")
	if err := os.Rename(filepath.Join(root, "docs"), outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	testutil.WriteFile(t, filepath.Join(outside, "guide.md"), "edited while outside")
	testutil.WriteFile(t, filepath.Join(outside, "added.md"), "added while outside")
	if err := os.Rename(outside, filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	checkpoint := takeCheckpoint(t, projectTracker, "2")
	checkMatchesScan(t, projectStore, root, checkpoint.ID)
}

func TestRestoreOfDirectoryMovedOutEditedAndBack(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-aside")
	if err := os.Rename(filepath.Join(root, "docs"), outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	testutil.WriteFile(t, filepath.Join(outside, "guide.md"), "edited while outside")
	if err := os.Rename(outside, filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	saved := takeCheckpoint(t, projectTracker, "2")
	if err := os.RemoveAll(filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, projectTracker, "3")
	if _, err := restore.Apply(projectStore, saved.ID, checkpointThroughTracker(projectTracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "docs", "guide.md")); string(data) != "edited while outside" {
		t.Fatalf("docs/guide.md = %q after restoring checkpoint 2", data)
	}
}

// A stored copy lost in a power cut, then merged away. The tracker's listing
// and the saved listing still hold the file, so no later checkpoint stores it
// again, while State no longer has it.
func TestRestoreNeverDeletesFileWhoseCopyWasLostAndMerged(t *testing.T) {
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	takeCheckpoint(t, projectTracker, "1")
	testutil.WriteFile(t, filepath.Join(root, "docs", "guide.md"), "precious v2")
	second := takeCheckpoint(t, projectTracker, "2")
	layerCopy := filepath.Join(projectStore.Dir(), "layers", strconv.Itoa(second.ID), "files", "docs", "guide.md")
	if err := os.Remove(layerCopy); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		testutil.WriteFile(t, filepath.Join(root, "README.md"), "edit "+strconv.Itoa(i))
		takeCheckpoint(t, projectTracker, "later")
	}
	projectTracker.Close()
	tracker2, err := newTracker(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker2.Close)
	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 2, MaxBytes: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := projectStore.Latest()
	if _, err := restore.Apply(projectStore, latest.ID, checkpointThroughTracker(tracker2)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "guide.md"))
	if err != nil {
		after, _, _ := projectStore.Latest()
		state, _ := projectStore.State(after.ID)
		_, inSafety := state["docs/guide.md"]
		t.Fatalf("restoring the latest checkpoint (unchanged project) removed docs/guide.md; safety checkpoint %d holds it: %v", after.ID, inSafety)
	}
	if string(data) != "precious v2" {
		t.Fatalf("guide.md = %q", data)
	}
}
