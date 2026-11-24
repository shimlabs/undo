package restore

import (
	"errors"
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
			err := filepath.WalkDir(dir, func(entryPath string, _ fs.DirEntry, _ error) error {
				return unix.Chflags(entryPath, 0)
			})
			if err != nil {
				t.Error(err)
			}
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

	requireWorksAfterPrune(t, projectStore, root)
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

	requireWorksAfterPrune(t, projectStore, root)
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
	testutil.AssertContent(t, filepath.Join(root, "src", "main.go"), "package main\n")
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
	t.Cleanup(func() {
		if err := setSymlinkFlags(0); err != nil {
			t.Error(err)
		}
	})
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
	testutil.AssertContent(t, filepath.Join(root, "src", "main.go"), "package main\n")
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

func TestRestoreKeepsFilesInFolderLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	docs := filepath.Join(root, "docs")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v1")
	lockFile(t, docs, root, projectStore.Dir())
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v2")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")

	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Kept, "docs/a.txt") {
		t.Errorf("Kept = %v, want docs/a.txt, which sits in a locked folder", changes.Kept)
	}
	for rel, want := range map[string]string{"src/main.go": "package main\n", "README.md": "# demo\n"} {
		testutil.AssertContent(t, filepath.Join(root, filepath.FromSlash(rel)), want)
	}
}

func TestRestoreKeepsFilesInProjectLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "new")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")
	lockFile(t, root, root, projectStore.Dir())

	changes := apply(t, projectStore, 1)
	for _, rel := range []string{"README.md", "new.txt"} {
		if !slices.Contains(changes.Kept, rel) {
			t.Errorf("Kept = %v, want %s, which sits in the locked project folder", changes.Kept, rel)
		}
	}
}

func TestRestoreDeletedFolderThatWasLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	docs := filepath.Join(root, "docs")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v1")
	lockFile(t, docs, root, projectStore.Dir())
	take(t, projectStore, "1")
	if err := unix.Chflags(docs, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(docs); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "2")

	apply(t, projectStore, 1)
	testutil.AssertContent(t, filepath.Join(docs, "a.txt"), "v1")
	if restored, err := projectStore.RestoredEntries(1); err != nil || len(restored) == 0 {
		t.Fatalf("restore record = %v, %v, want it saved", restored, err)
	}
}

func TestRestoreDeletedTreeWithFolderLockedInFinder(t *testing.T) {
	projectStore, root := newProject(t)
	signed := filepath.Join(root, "assets", "signed")
	testutil.WriteFile(t, filepath.Join(signed, "a.pdf"), "v1")
	lockFile(t, signed, root, projectStore.Dir())
	take(t, projectStore, "1")
	if err := unix.Chflags(signed, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "assets")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")

	apply(t, projectStore, 1)
	apply(t, projectStore, 2)
	if _, err := os.Lstat(filepath.Join(root, "assets")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("assets after undoing the restore: %v, want it gone", err)
	}
}

func TestRestoreFolderLockedInFinderKeepsModesAndTimes(t *testing.T) {
	projectStore, root := newProject(t)
	docs := filepath.Join(root, "docs")
	testutil.WriteFile(t, filepath.Join(docs, "a.txt"), "v1")
	if err := os.Chmod(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	lockFile(t, docs, root, projectStore.Dir())
	want := snapshot(t, root)
	take(t, projectStore, "1")
	if err := unix.Chflags(docs, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(docs); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreUndoesCaseRenameOfFolderHoldingExcludedContent(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "dep", "index.js"), "dep")
	testutil.WriteFile(t, filepath.Join(root, "web", "app.js"), "app")
	first := take(t, projectStore, "1")
	if err := os.Rename(filepath.Join(root, "web"), filepath.Join(root, "Web")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "2")

	apply(t, projectStore, first.ID)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "Web" {
			t.Fatal("the folder is still named Web, want web as at the checkpoint")
		}
	}
}

// renameThenLock renames from to to, a name that differs only in case or
// Unicode form, locks it, and checks that restoring the checkpoint from
// before the rename still restores everything else.
func renameThenLock(t *testing.T, from, to string) {
	t.Helper()
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, from, "a.go"), "v1")
	take(t, projectStore, "1")
	if err := os.Rename(filepath.Join(root, from), filepath.Join(root, to)); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "after")
	lockFile(t, filepath.Join(root, to), root, projectStore.Dir())
	take(t, projectStore, "2")

	apply(t, projectStore, 1)
	testutil.AssertContent(t, filepath.Join(root, "README.md"), "# demo\n")
}

func TestRestoreWithLockedFolderRenamedInCase(t *testing.T) {
	renameThenLock(t, "lib", "Lib")
}

func TestRestoreWithLockedFolderRenamedInUnicodeForm(t *testing.T) {
	renameThenLock(t, "café", "café")
}

func TestRestoreWithLockedFileRenamedInCase(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "v1")
	take(t, projectStore, "1")
	if err := os.Rename(filepath.Join(root, "notes.txt"), filepath.Join(root, "NOTES.txt")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	lockFile(t, filepath.Join(root, "NOTES.txt"), root, projectStore.Dir())
	take(t, projectStore, "2")

	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Kept, "notes.txt") {
		t.Errorf("Kept = %v, want notes.txt, which is locked under another case", changes.Kept)
	}
	testutil.AssertContent(t, filepath.Join(root, "src", "main.go"), "package main\n")
}

// A folder in a locked folder whose only change is its times: a restore has
// nothing to do there, and must not ask the user to unlock anything.
func TestRestoreLeavesFolderWithOnlyNewTimesInLockedFolder(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "vault", "inbox", "a.txt"), "a")
	lockFile(t, filepath.Join(root, "vault"), root, projectStore.Dir())
	first := take(t, projectStore, "1")
	makeTemporaryFile(t, filepath.Join(root, "vault", "inbox"))

	changes, err := Plan(projectStore, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Written)+len(changes.Removed)+len(changes.Kept) != 0 {
		t.Fatalf("written %v, kept %v for a folder whose only change is its times", changes.Written, changes.Kept)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "changed")
	if applied := apply(t, projectStore, first.ID); len(applied.Kept) != 0 {
		t.Fatalf("kept %v along with a real change", applied.Kept)
	}
}
