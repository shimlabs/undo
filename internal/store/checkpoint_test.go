package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/tree"
)

func take(t *testing.T, projectStore *Store, label string) Checkpoint {
	t.Helper()
	checkpoint, err := projectStore.Take(CheckpointOptions{Label: label, Dir: projectStore.Root()})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func newProject(t *testing.T, files map[string]string) (*Store, string) {
	t.Helper()
	root := testutil.TempDir(t)
	for path, content := range files {
		testutil.WriteFile(t, filepath.Join(root, path), content)
	}
	projectStore, err := Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return projectStore, root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFirstTakeClonesBase(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha"})

	checkpoint := take(t, projectStore, "first")

	if checkpoint.ID != 1 {
		t.Fatalf("first checkpoint id = %d, want 1", checkpoint.ID)
	}
	if got := readFile(t, projectStore.basePath("a.txt")); got != "alpha" {
		t.Fatalf("base a.txt = %q", got)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || checkpoints[0].Label != "first" || checkpoints[0].Dir != root {
		t.Fatalf("List() = %+v", checkpoints)
	}
}

func TestTakeRecordsChangedAndDeleted(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	take(t, projectStore, "first")

	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "alpha two")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "c", "d.txt"), "delta")

	checkpoint := take(t, projectStore, "second")
	manifest, err := projectStore.Manifest(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"a.txt", "c", "c/d.txt"} {
		if _, ok := manifest.Changed[path]; !ok {
			t.Errorf("manifest missing changed path %q", path)
		}
	}
	if !slices.Equal(manifest.Deleted, []string{"b.txt"}) {
		t.Errorf("deleted = %v, want [b.txt]", manifest.Deleted)
	}
	if got := readFile(t, projectStore.layerFile(2, "a.txt")); got != "alpha two" {
		t.Errorf("layer a.txt = %q", got)
	}
}

func TestTakeIgnoresExcluded(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"node_modules/pkg/index.js": "v1", "main.js": "x"})
	take(t, projectStore, "first")
	if _, err := os.Stat(projectStore.basePath("node_modules")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("node_modules was cloned into the base")
	}

	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "v2")
	checkpoint := take(t, projectStore, "second")
	manifest, err := projectStore.Manifest(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changed) != 0 || len(manifest.Deleted) != 0 {
		t.Fatalf("excluded change recorded: %+v", manifest)
	}
}

func TestTakeLockedConcurrently(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha"})
	take(t, projectStore, "first")

	var wg sync.WaitGroup
	ids := make([]int, 2)
	for i := range ids {
		wg.Go(func() {
			other, err := Open(filepath.Dir(projectStore.Dir()), root)
			if err != nil {
				t.Error(err)
				return
			}
			checkpoint, err := other.Take(CheckpointOptions{Label: "parallel", Dir: root})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = checkpoint.ID
		})
	}
	wg.Wait()

	slices.Sort(ids)
	if !slices.Equal(ids, []int{2, 3}) {
		t.Fatalf("ids = %v, want [2 3]", ids)
	}
}

func TestFirstTakeNeverReadsExcluded(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"main.js": "x", "lib/util.js": "u"})
	locked := filepath.Join(root, "node_modules", "locked")
	testutil.WriteFile(t, filepath.Join(locked, "secret.js"), "s")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	if _, err := projectStore.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatalf("first checkpoint read an excluded directory: %v", err)
	}
	if got := readFile(t, projectStore.basePath("lib/util.js")); got != "u" {
		t.Fatalf("base lib/util.js = %q", got)
	}
}

func TestListingAtRebuildsWhenSavedCopyIsMissing(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "alpha two")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	checkpoint := take(t, projectStore, "second")
	saved, err := projectStore.ListingAt(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(projectStore.listingPath()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := projectStore.ListingAt(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(rebuilt) != len(saved) {
		t.Fatalf("rebuilt %d paths, saved %d", len(rebuilt), len(saved))
	}
	for rel, entry := range saved {
		if rebuilt[rel] != entry {
			t.Fatalf("%s: rebuilt %+v, saved %+v", rel, rebuilt[rel], entry)
		}
	}
}

func TestCommitRecordsGivenChanges(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	first := take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "alpha two")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}

	current, err := projectStore.ListingAt(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := tree.Stat(filepath.Join(root, "a.txt"))
	current["a.txt"] = entry
	delete(current, "b.txt")

	checkpoint, err := projectStore.Commit(first.ID, CheckpointOptions{Label: "from watcher", Dir: root}, Changes{Listing: current, Changed: []string{"a.txt"}, Deleted: []string{"b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := projectStore.Manifest(checkpoint.ID)
	if _, ok := manifest.Changed["a.txt"]; !ok || !slices.Equal(manifest.Deleted, []string{"b.txt"}) {
		t.Fatalf("manifest = %+v", manifest)
	}
	if got := readFile(t, projectStore.layerFile(checkpoint.ID, "a.txt")); got != "alpha two" {
		t.Fatalf("layer a.txt = %q", got)
	}
}

func TestCommitRefusesStaleChanges(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "alpha"})
	first := take(t, projectStore, "first")
	take(t, projectStore, "second")
	current, _ := projectStore.ListingAt(first.ID)

	if _, err := projectStore.Commit(first.ID, CheckpointOptions{Label: "stale", Dir: root}, Changes{Listing: current}); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestTakeSkipsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	projectStore, root := newProject(t, map[string]string{"a.txt": "a", "pgdata/base/1": "rows"})
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })

	checkpoint, err := projectStore.Take(CheckpointOptions{Label: "first", Dir: root})
	if err != nil {
		t.Fatalf("one unreadable directory failed the checkpoint: %v", err)
	}
	manifest, _ := projectStore.Manifest(checkpoint.ID)
	if !slices.Equal(manifest.Unreadable, []string{"pgdata"}) {
		t.Fatalf("unreadable = %v, want [pgdata]", manifest.Unreadable)
	}
}

func TestTakeProtectsFilesNamedLikeExcludedDirectories(t *testing.T) {
	projectStore, _ := newProject(t, map[string]string{"target": "important", ".cache": "x"})

	checkpoint := take(t, projectStore, "first")
	state, _ := projectStore.State(checkpoint.ID)
	if _, ok := state["target"]; !ok {
		t.Fatal("a regular file named target is not protected")
	}
}

func TestTakeShortensLongLabels(t *testing.T) {
	projectStore, _ := newProject(t, map[string]string{"a.txt": "a"})
	heredoc := "cat > big.txt <<EOF\n" + strings.Repeat("line\n", 1000) + "EOF"

	checkpoint := take(t, projectStore, heredoc)
	if checkpoint.Label != "cat > big.txt <<EOF …" {
		t.Fatalf("label = %q", checkpoint.Label)
	}
	if long := strings.Repeat("x", 500); shortLabel(long) != strings.Repeat("x", maxLabelLength)+" …" {
		t.Fatal("a long single-line label was not cut")
	}
}

func TestTakeClearsLeftoverLayer(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	take(t, projectStore, "first")
	testutil.WriteFile(t, projectStore.layerFile(2, "stale.txt"), "left by a crash")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a2")

	checkpoint := take(t, projectStore, "second")
	if _, err := os.Stat(projectStore.layerFile(checkpoint.ID, "stale.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a file from an interrupted checkpoint was kept in the new layer")
	}
}

func TestLatestIgnoresLineCutOffByCrash(t *testing.T) {
	projectStore, _ := newProject(t, map[string]string{"a.txt": "a"})
	first := take(t, projectStore, "first")
	file, _ := os.OpenFile(projectStore.indexPath(), os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(`{"id":2,"time":"2026-`)
	file.Close()

	latest, ok, err := projectStore.Latest()
	if err != nil || !ok || latest.ID != first.ID {
		t.Fatalf("Latest = %+v, %v, %v; want checkpoint %d", latest, ok, err, first.ID)
	}
	if checkpoints, err := projectStore.List(); err != nil || len(checkpoints) != 1 {
		t.Fatalf("List = %v, %v", checkpoints, err)
	}

	for _, label := range []string{"second", "third"} {
		take(t, projectStore, label)
	}
	checkpoints, err := projectStore.List()
	if err != nil || len(checkpoints) != 3 || checkpoints[2].ID != 3 {
		t.Fatalf("List after two more checkpoints = %v, %v", checkpoints, err)
	}
}

func TestMergeIntoReadOnlyDirectory(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"ro/f.txt": "v1"})
	if err := os.Chmod(filepath.Join(root, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "ro"), 0o755) })
	for _, content := range []string{"v1", "v2", "v3"} {
		testutil.WriteFile(t, filepath.Join(root, "ro", "f.txt"), content)
		take(t, projectStore, content)
	}

	if _, err := projectStore.Prune(time.Now(), Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("Prune into a read-only directory: %v", err)
	}
	testutil.WriteFile(t, filepath.Join(root, "ro", "f.txt"), "v4")
	if _, err := projectStore.Take(CheckpointOptions{Label: "v4", Dir: root}); err != nil {
		t.Fatalf("checkpoint after the merge: %v", err)
	}
}

func TestFailedMergeKeepsCheckpointsWorking(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	take(t, projectStore, "first")
	if err := writeJSON(projectStore.mergePath(), pendingMerge{Through: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectStore.manifestPath(1)); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a2")
	if _, err := projectStore.Take(CheckpointOptions{Label: "second", Dir: root}); err != nil {
		t.Fatalf("a failing merge stopped checkpoints: %v", err)
	}
	if _, err := projectStore.State(2); !errors.Is(err, ErrMergePending) {
		t.Fatalf("State = %v, want ErrMergePending", err)
	}
	if !strings.Contains(projectStore.LastError(), "merge") {
		t.Fatalf("the merge failure was not logged: %q", projectStore.LastError())
	}
}

func TestPruneIfDueRunsHourly(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a"})
	for i := range 3 {
		testutil.WriteFile(t, filepath.Join(root, "a.txt"), strings.Repeat("a", i+2))
		take(t, projectStore, "edit")
	}
	keepOne := Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	now := time.Now()

	if merged, err := projectStore.PruneIfDue(now, Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 1 << 40}); err != nil || merged != 0 {
		t.Fatalf("first PruneIfDue = %d, %v", merged, err)
	}
	if merged, _ := projectStore.PruneIfDue(now.Add(time.Minute), keepOne); merged != 0 {
		t.Fatalf("PruneIfDue pruned again within the hour: merged %d", merged)
	}
	if merged, _ := projectStore.PruneIfDue(now.Add(2*time.Hour), keepOne); merged != 2 {
		t.Fatalf("PruneIfDue after an hour merged %d, want 2", merged)
	}
}

func TestUnstableFileIsSavedAgainOnceItSettles(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"log.txt": ""})
	path := filepath.Join(root, "log.txt")
	unstableCount := 0
	for attempt := range 100 {
		var stop atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return
			}
			defer file.Close()
			for !stop.Load() {
				file.WriteString("line\n")
			}
		}()
		checkpoint, err := projectStore.Take(CheckpointOptions{Label: fmt.Sprint(attempt), Dir: root})
		stop.Store(true)
		<-done
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := projectStore.Manifest(checkpoint.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(manifest.Unstable, "log.txt") {
			continue
		}
		if indexed, err := projectStore.Checkpoint(checkpoint.ID); err != nil || indexed.UnstableCount != len(manifest.Unstable) {
			t.Fatalf("index line says %d unstable files, the manifest %d: %v", indexed.UnstableCount, len(manifest.Unstable), err)
		}

		settled := take(t, projectStore, "settled")
		state, err := projectStore.State(settled.ID)
		if err != nil {
			t.Fatal(err)
		}
		saved, _ := os.ReadFile(projectStore.layerFile(state["log.txt"].Layer, "log.txt"))
		current, _ := os.ReadFile(path)
		if len(saved) != len(current) {
			t.Fatalf("checkpoint after the file settled holds %d bytes, the file has %d", len(saved), len(current))
		}
		unstableCount++
	}
	if unstableCount == 0 {
		t.Skip("the file never changed during a clone")
	}
}

func TestListingAtBuildsForwardFromOlderSavedListing(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a", "dir/b.txt": "b"})
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a2")
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "new", "c.txt"), "c")
	latest := take(t, projectStore, "second")
	if id, _ := projectStore.savedListingID(); id != 1 {
		t.Fatalf("saved listing is at checkpoint %d, want it left at 1 until the chain is long", id)
	}

	forward, err := projectStore.ListingAt(latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := projectStore.State(latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward) != len(state) {
		t.Fatalf("forward listing has %d paths, state has %d", len(forward), len(state))
	}
	for rel, source := range state {
		if forward[rel] != source.Entry {
			t.Errorf("%s: forward %+v, state %+v", rel, forward[rel], source.Entry)
		}
	}
}
