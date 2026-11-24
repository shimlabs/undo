package restore

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

// newRewrittenProject returns a project of fileCount files in folderCount
// folders that a restore to checkpoint 2 wrote back as new clones.
// Checkpoint 1 holds the same entries, so planning a restore to it compares
// every file with its stored copy.
func newRewrittenProject(tb testing.TB, fileCount, folderCount int) *store.Store {
	tb.Helper()
	root := testutil.TempDir(tb)
	for i := range fileCount {
		testutil.WriteFile(tb, rewrittenFilePath(root, i, folderCount), "package main")
	}
	projectStore, err := store.Create(tb.TempDir(), root)
	if err != nil {
		tb.Fatal(err)
	}
	take(tb, projectStore, "first")
	testutil.WriteFile(tb, filepath.Join(root, "notes.txt"), "second")
	take(tb, projectStore, "second")
	for i := range fileCount {
		testutil.WriteFile(tb, rewrittenFilePath(root, i, folderCount), "changed")
	}
	apply(tb, projectStore, 2)
	return projectStore
}

// rewrittenFilePath returns the path of the file numbered i in a project
// newRewrittenProject makes.
func rewrittenFilePath(root string, i, folderCount int) string {
	return filepath.Join(root, fmt.Sprintf("dir%02d", i%folderCount), fmt.Sprintf("file%05d.go", i))
}

func planRewrittenProject(tb testing.TB, projectStore *store.Store) restorePlan {
	tb.Helper()
	plan, unlock, err := lockAndPlan(projectStore, 1)
	if err != nil {
		tb.Fatal(err)
	}
	unlock()
	return plan
}

func TestPlanComparesRewrittenFilesWithPooledBuffers(t *testing.T) {
	const fileCount = 200
	projectStore := newRewrittenProject(t, fileCount, 50)
	if plan := planRewrittenProject(t, projectStore); len(plan.unchanged) < fileCount {
		t.Fatalf("plan found %d unchanged paths, want at least %d", len(plan.unchanged), fileCount)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	planRewrittenProject(t, projectStore)
	runtime.ReadMemStats(&after)
	// Two 64 KB chunks for each file would allocate 25.6 MB.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > fileCount*64<<10 {
		t.Errorf("planning allocated %d KB to compare %d files", allocated>>10, fileCount)
	}
}

// BenchmarkPlanAfterRestore measures planning a restore that compares 10,000
// files a restore wrote back, in 50 folders.
func BenchmarkPlanAfterRestore(b *testing.B) {
	projectStore := newRewrittenProject(b, 10_000, 50)
	for b.Loop() {
		planRewrittenProject(b, projectStore)
	}
}

// BenchmarkPlanAfterRestoreInOneFolder measures the same with every file in
// one folder, as in generated assets or test fixtures.
func BenchmarkPlanAfterRestoreInOneFolder(b *testing.B) {
	projectStore := newRewrittenProject(b, 10_000, 1)
	for b.Loop() {
		planRewrittenProject(b, projectStore)
	}
}

// The restore record already says which paths still hold what the last
// restore wrote, so planning the same restore again reads none of them.
func TestPlanAfterRestoreComparesNoContent(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	for _, dir := range []string{"src", ".git"} {
		if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
			t.Fatal(err)
		}
	}
	apply(t, projectStore, 1)

	unlock, err := projectStore.RLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	inputs, err := loadRestoreInputs(projectStore, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan := restorePlan{root: root, target: inputs.target, current: maps.Clone(inputs.current)}
	if compared := plan.rewrittenPaths(projectStore, inputs.restored); len(compared) != 0 {
		t.Fatalf("planning again read %v, which the restore record covers", slices.Sorted(maps.Keys(compared)))
	}
}
