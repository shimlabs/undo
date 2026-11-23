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
			filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
				if err == nil {
					unix.Chflags(path, 0)
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
