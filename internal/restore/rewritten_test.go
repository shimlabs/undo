package restore

import (
	"fmt"
	"path/filepath"
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

// BenchmarkPlanAfterRestore measures planning a restore that compares 10,000
// files a restore wrote back, in 50 folders.
func BenchmarkPlanAfterRestore(b *testing.B) {
	projectStore := newRewrittenProject(b, 10_000)
	for b.Loop() {
		planRewrittenProject(b, projectStore)
	}
}
