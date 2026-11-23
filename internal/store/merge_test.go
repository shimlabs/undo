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
		data, _ := os.ReadFile(path)
		hashes[rel] = sha256.Sum256(append([]byte(source.Entry.Mode.String()), data...))
	}
	return hashes
}

func TestPruneFinishesInterruptedMerge(t *testing.T) {
	projectStore, root := newProject(t, nil)
	steps := []func(){
		func() { os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0o644) },
		func() {
			os.Mkdir(filepath.Join(root, "dir"), 0o755)
			os.WriteFile(filepath.Join(root, "dir", "b.txt"), []byte("two"), 0o644)
		},
		func() { os.Remove(filepath.Join(root, "a.txt")) },
		func() {
			os.RemoveAll(filepath.Join(root, "dir"))
			os.WriteFile(filepath.Join(root, "dir"), []byte("now a file"), 0o644)
		},
	}
	for _, step := range steps {
		step()
		if _, err := projectStore.Take(CheckpointOptions{Label: "step", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
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
	list, _ := projectStore.List()
	if len(list) != 2 || list[0].ID != 3 {
		t.Fatalf("checkpoints = %+v, want 3 and 4", list)
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
	go func() {
		projectStore.Prune(time.Now(), DefaultLimits())
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
}

func TestFinishMergeNeverRemovesThroughBaseSymlink(t *testing.T) {
	projectStore, root := newProject(t, map[string]string{"a/x": "project"})
	outside := t.TempDir()
	testutil.WriteFile(t, filepath.Join(outside, "x"), "outside")
	if _, err := projectStore.Take(CheckpointOptions{Label: "first"}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "a"))
	if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := projectStore.Take(CheckpointOptions{Label: "second"}); err != nil {
		t.Fatal(err)
	}
	// An interrupted merge can leave the base half updated: here layer 2
	// already turned a into a symlink, and the record says to replay it.
	os.RemoveAll(projectStore.basePath("a"))
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
	projectStore, root := newProject(t, map[string]string{"main.go": "package main", ".git/HEAD": "ref"})
	if _, err := projectStore.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
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
		if _, err := projectStore.Take(CheckpointOptions{Label: content, Dir: root}); err != nil {
			t.Fatal(err)
		}
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
