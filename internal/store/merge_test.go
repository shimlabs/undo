package store

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/tree"
)

func stateHashes(t *testing.T, projectStore *Store, id int) map[string][32]byte {
	t.Helper()
	state, err := projectStore.State(id)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string][32]byte{}
	for rel, source := range state {
		path := projectStore.basePath(rel)
		if source.Layer > 0 {
			path = projectStore.layerFile(source.Layer, rel)
		}
		var data []byte
		if source.Entry.Kind == tree.File {
			if data, err = os.ReadFile(path); err != nil {
				t.Fatal(err)
			}
		}
		hashes[rel] = sha256.Sum256(append([]byte(source.Entry.Mode.String()), data...))
	}
	return hashes
}

func TestPruneFinishesInterruptedMerge(t *testing.T) {
	projectStore, root := newProject(t, nil)
	testutil.History(t, root, func(label string) { take(t, projectStore, label) })
	wantAtThird, wantAtFourth := stateHashes(t, projectStore, 3), stateHashes(t, projectStore, 4)

	base, err := projectStore.baseListing()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(projectStore.mergePath(), pendingMerge{Through: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.mergeLayer(2, base, tree.NewParentChecker(projectStore.baseDir())); err != nil {
		t.Fatal(err)
	}

	unlimited := Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 1 << 40}
	if _, err := projectStore.Prune(time.Now(), unlimited); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 2 || checkpoints[0].ID != 3 {
		t.Fatalf("checkpoints = %+v, want 3 and 4", checkpoints)
	}
	for id, want := range map[int]map[string][32]byte{3: wantAtThird, 4: wantAtFourth} {
		got := stateHashes(t, projectStore, id)
		if len(got) != len(want) {
			t.Fatalf("state %d has %d paths, want %d", id, len(got), len(want))
		}
		for rel, hash := range want {
			if got[rel] != hash {
				t.Errorf("state %d: %s differs", id, rel)
			}
		}
	}
	if _, err := os.Stat(projectStore.mergePath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("merge record left behind: %v", err)
	}
}

func TestPruneWaitsForReaders(t *testing.T) {
	projectStore, _ := newProject(t, nil)
	unlock, err := projectStore.RLock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	pruneErr := make(chan error, 1)
	go func() {
		_, err := projectStore.Prune(time.Now(), DefaultLimits())
		pruneErr <- err
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Prune ran while a reader held the store")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Prune did not run after the reader finished")
	}
	if err := <-pruneErr; err != nil {
		t.Fatal(err)
	}
}

func TestFinishMergeNeverRemovesThroughBaseSymlink(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a/x": "project"})
	outside := t.TempDir()
	testutil.WriteFile(t, filepath.Join(outside, "x"), "outside")
	take(t, projectStore, "first")
	if err := os.RemoveAll(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "second")
	// An interrupted merge can leave the base half updated: here layer 2
	// already turned a into a symlink, and the record says to replay it.
	if err := os.RemoveAll(projectStore.basePath("a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, projectStore.basePath("a")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(projectStore.mergePath(), pendingMerge{Through: 2}); err != nil {
		t.Fatal(err)
	}

	if err := projectStore.finishMerge(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "x")); err != nil || string(data) != "outside" {
		t.Fatalf("merge changed a file outside the store: %q, %v", data, err)
	}
	if target, err := os.Readlink(projectStore.basePath("a")); err != nil || target != outside {
		t.Fatalf("base a = %q, %v; want the symlink from layer 2", target, err)
	}
}

func TestRemoveUnlistedLeavesOnlyListedPathsInBase(t *testing.T) {
	projectStore, _ := newProject(t, map[string]string{"main.go": "package main", ".git/HEAD": "ref"})
	take(t, projectStore, "first")
	// A file created and removed while the base was cloned is in the base
	// but in no listing.
	testutil.WriteFile(t, projectStore.basePath(".git/index.lock"), "")
	testutil.WriteFile(t, projectStore.basePath("gone/file"), "")
	listing, err := projectStore.baseListing()
	if err != nil {
		t.Fatal(err)
	}

	if err := projectStore.removeUnlisted(listing); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".git/index.lock", "gone"} {
		if _, err := os.Lstat(projectStore.basePath(rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("base still holds %s: %v", rel, err)
		}
	}
	if _, err := os.Lstat(projectStore.basePath(".git/HEAD")); err != nil {
		t.Errorf("base lost a listed file: %v", err)
	}
}

func TestFinishMergeRemovesFilesLeftByInterruptedMerge(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"dir/a.txt": "one"})
	for _, content := range []string{"two", "three"} {
		testutil.WriteFile(t, filepath.Join(root, "dir", "a.txt"), content)
		take(t, projectStore, content)
	}
	leftover := projectStore.basePath("dir/.a.txt.undo-0123456789ab")
	testutil.WriteFile(t, leftover, "half written")
	if err := writeJSON(projectStore.mergePath(), pendingMerge{Through: 1}); err != nil {
		t.Fatal(err)
	}

	if err := projectStore.finishMerge(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(leftover); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("base still holds the interrupted merge's temporary file: %v", err)
	}
}

// After a power cut, the newest layer's cloned files may be missing even
// though its manifest was flushed. Once that layer is merged, the merge
// fails, merge.json stays, and every State call returns ErrMergePending.
func TestMissingLayerFileDoesNotBlockStore(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a.txt": "a1", "b.txt": "b1"})
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "a.txt"), "a2")
	second := take(t, projectStore, "2")
	if err := os.Remove(projectStore.layerFile(second.ID, "a.txt")); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		testutil.WriteFile(t, filepath.Join(root, "b.txt"), "b"+string(rune('2'+i)))
		take(t, projectStore, "later")
	}
	_, pruneErr := projectStore.Prune(time.Now(), Limits{MaxAge: time.Hour, MaxCount: 2, MaxBytes: 1 << 40})
	latest, _, err := projectStore.Latest()
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "c.txt"), "new work")
	if _, err := projectStore.Take(CheckpointOptions{Label: "after", Dir: root}); err != nil {
		t.Errorf("Take after the failed merge: %v", err)
	}
	if _, err := projectStore.State(latest.ID); err != nil {
		t.Fatalf("prune error %v; State(latest=%d) = %v", pruneErr, latest.ID, err)
	}
}
