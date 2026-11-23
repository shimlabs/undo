package store_test

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

func newEmptyProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := testutil.TempDir(t)
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return projectStore, root
}

// history takes four checkpoints with different files and returns the
// project's fingerprint at each of them.
func history(t *testing.T, projectStore *store.Store, root string) []map[string]string {
	t.Helper()
	return testutil.History(t, root, func(label string) {
		if _, err := projectStore.Take(store.CheckpointOptions{Label: label, Dir: root}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPruneMergesOldestIntoBase(t *testing.T) {
	projectStore, root := newEmptyProject(t)
	states := history(t, projectStore, root)

	merged, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 2, MaxBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if merged != 2 {
		t.Fatalf("merged %d checkpoints, want 2", merged)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 2 || checkpoints[0].ID != 3 || checkpoints[1].ID != 4 {
		t.Fatalf("remaining checkpoints = %+v", checkpoints)
	}
	for _, id := range []int{3, 4} {
		if _, err := restore.Apply(projectStore, id, (*store.Store).Take); err != nil {
			t.Fatal(err)
		}
		if got := testutil.Fingerprint(t, root); !maps.Equal(got, states[id-1]) {
			t.Fatalf("restore to %d after pruning:\n got %v\nwant %v", id, got, states[id-1])
		}
	}
}

func TestPruneByAgeKeepsLatest(t *testing.T) {
	projectStore, root := newEmptyProject(t)
	history(t, projectStore, root)

	if _, err := projectStore.Prune(time.Now().Add(8*24*time.Hour), store.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != 4 {
		t.Fatalf("remaining checkpoints = %+v, want only 4", checkpoints)
	}
}

func TestUsageCountsOnlyUnsharedData(t *testing.T) {
	projectStore, root := newEmptyProject(t)
	data := strings.Repeat("x", 4<<20)
	testutil.WriteFile(t, filepath.Join(root, "big.bin"), data)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}

	shared, err := projectStore.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if shared > 1<<20 {
		t.Fatalf("usage %d bytes while the base shares all data with the project", shared)
	}

	testutil.WriteFile(t, filepath.Join(root, "big.bin"), strings.Repeat("y", 4<<20))
	private, err := projectStore.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if private < 4<<20 {
		t.Fatalf("usage %d bytes after the project's copy was rewritten, want at least 4 MiB", private)
	}
}

func TestPruneBySizeMergesUntilUnder(t *testing.T) {
	projectStore, root := newEmptyProject(t)
	for _, fill := range []string{"a", "b", "c"} {
		testutil.WriteFile(t, filepath.Join(root, "big.bin"), strings.Repeat(fill, 2<<20))
		if _, err := projectStore.Take(store.CheckpointOptions{Label: "write " + fill, Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	testutil.WriteFile(t, filepath.Join(root, "big.bin"), strings.Repeat("d", 2<<20))
	want := testutil.Fingerprint(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "write d", Dir: root}); err != nil {
		t.Fatal(err)
	}
	before, err := projectStore.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if before < 6<<20 {
		t.Fatalf("usage %d before pruning, want at least 6 MiB of old versions", before)
	}

	merged, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 3 << 20})
	if err != nil {
		t.Fatal(err)
	}
	after, err := projectStore.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if merged == 0 || after > 3<<20 {
		t.Fatalf("merged %d, usage %d after pruning, want at most 3 MiB", merged, after)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoints[len(checkpoints)-1].ID != 4 {
		t.Fatalf("latest checkpoint is %d, want 4", checkpoints[len(checkpoints)-1].ID)
	}
	if _, err := restore.Apply(projectStore, 4, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Fingerprint(t, root); !maps.Equal(got, want) {
		t.Fatal("restore to the latest checkpoint differs after pruning by size")
	}
}
