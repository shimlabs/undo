package restore

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

// newRewrittenProject returns a project of fileCount files that a restore to
// checkpoint 2 wrote back as new clones. Checkpoint 1 holds the same entries,
// so planning a restore to it compares every file with its stored copy.
func newRewrittenProject(tb testing.TB, fileCount int) *store.Store {
	tb.Helper()
	root := testutil.TempDir(tb)
	for i := range fileCount {
		testutil.WriteFile(tb, filepath.Join(root, fmt.Sprintf("dir%02d", i%50), fmt.Sprintf("file%05d.go", i)), "package main")
	}
	projectStore, err := store.Create(tb.TempDir(), root)
	if err != nil {
		tb.Fatal(err)
	}
	take(tb, projectStore, "first")
	testutil.WriteFile(tb, filepath.Join(root, "notes.txt"), "second")
	take(tb, projectStore, "second")
	for i := range fileCount {
		testutil.WriteFile(tb, filepath.Join(root, fmt.Sprintf("dir%02d", i%50), fmt.Sprintf("file%05d.go", i)), "changed")
	}
	apply(tb, projectStore, 2)
	return projectStore
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

func TestPlanComparesRewrittenFilesWithoutBuffersForEach(t *testing.T) {
	const fileCount = 200
	projectStore := newRewrittenProject(t, fileCount)
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
	projectStore := newRewrittenProject(b, 10_000)
	for b.Loop() {
		planRewrittenProject(b, projectStore)
	}
}
