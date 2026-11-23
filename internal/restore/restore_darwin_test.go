package restore

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

// lockFile sets the flag Finder's Locked checkbox sets, and clears it from
// everything under the test's directories when the test ends.
func lockFile(t *testing.T, path string, dirs ...string) {
	t.Helper()
	if err := unix.Chflags(path, unix.UF_IMMUTABLE); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, dir := range dirs {
			filepath.WalkDir(dir, func(entryPath string, _ fs.DirEntry, err error) error {
				if err == nil {
					unix.Chflags(entryPath, 0)
				}
				return nil
			})
		}
	})
}

func TestPruneAndRestoreWithFileLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	locked := filepath.Join(root, "contract.pdf")
	testutil.WriteFile(t, locked, "signed")
	lockFile(t, locked, root, projectStore.Dir())
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "x")
	take(t, projectStore, "3")

	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "4")
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	apply(t, projectStore, checkpoints[len(checkpoints)-2].ID)
	if data, err := os.ReadFile(filepath.Join(root, "src", "main.go")); err != nil || string(data) != "package main\n" {
		t.Fatalf("src/main.go = %q, %v", data, err)
	}
}

func TestPruneWithFileLockedThenUnlockedAndEdited(t *testing.T) {
	projectStore, root := newProject(t)
	locked := filepath.Join(root, "notes.txt")
	testutil.WriteFile(t, locked, "v1")
	lockFile(t, locked, root, projectStore.Dir())
	take(t, projectStore, "1")
	if err := unix.Chflags(locked, 0); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, locked, "v2")
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "x")
	take(t, projectStore, "3")

	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	take(t, projectStore, "4")
}

func TestRestoreKeepsFileLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	locked := filepath.Join(root, "README.md")
	testutil.WriteFile(t, locked, "# locked edit")
	lockFile(t, locked, root, projectStore.Dir())
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "2")

	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Kept, "README.md") {
		t.Errorf("Kept = %v, want the locked README.md", changes.Kept)
	}
	if data, err := os.ReadFile(filepath.Join(root, "src", "main.go")); err != nil || string(data) != "package main\n" {
		t.Errorf("src/main.go = %q, %v", data, err)
	}
}

// lockSymlink sets the lock flag on the symlink path itself, as chflags -h
// does, and clears it when the test ends.
func lockSymlink(t *testing.T, path string) {
	t.Helper()
	setSymlinkFlags := func(flags int) error {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_SYMLINK|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		return unix.Fchflags(fd, flags)
	}
	if err := setSymlinkFlags(unix.UF_IMMUTABLE); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setSymlinkFlags(0) })
}

// requireWorksAfterPrune merges all but the newest checkpoint, then checks
// that a checkpoint and a restore still work.
func requireWorksAfterPrune(t *testing.T, projectStore *store.Store, root string) {
	t.Helper()
	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	beforeDamage := take(t, projectStore, "before damage")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "damaged")
	apply(t, projectStore, beforeDamage.ID)
	if data, err := os.ReadFile(filepath.Join(root, "src", "main.go")); err != nil || string(data) != "package main\n" {
		t.Fatalf("src/main.go = %q, %v", data, err)
	}
}

func TestPruneWithFolderLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	docs := filepath.Join(root, "docs")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v1")
	lockFile(t, docs, root, projectStore.Dir())
	take(t, projectStore, "1")
	// A locked folder still lets its files change in place.
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v2")
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "x")
	take(t, projectStore, "3")

	requireWorksAfterPrune(t, projectStore, root)
}

func TestPruneWithFolderLockedThenUnlocked(t *testing.T) {
	projectStore, root := newProject(t)
	docs := filepath.Join(root, "docs")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v1")
	lockFile(t, docs, root, projectStore.Dir())
	take(t, projectStore, "1")
	if err := unix.Chflags(docs, 0); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(docs, "b.txt"), "new")
	if err := os.Remove(filepath.Join(docs, "a.txt")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "x")
	take(t, projectStore, "3")

	requireWorksAfterPrune(t, projectStore, root)
}

func TestPruneWithSymlinkLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	link := filepath.Join(root, "link")
	if err := os.Symlink("README.md", link); err != nil {
		t.Fatal(err)
	}
	lockSymlink(t, link)
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "x")
	take(t, projectStore, "3")

	requireWorksAfterPrune(t, projectStore, root)
}
