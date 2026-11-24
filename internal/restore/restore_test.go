package restore

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

func newProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := testutil.TempDir(t)
	files := map[string]string{
		"README.md":         "# demo\n",
		"src/main.go":       "package main\n",
		"src/util/str.go":   "package util\n",
		".git/HEAD":         "ref: refs/heads/main\n",
		".git/objects/ab/c": "blob",
	}
	for path, content := range files {
		testutil.WriteFile(t, filepath.Join(root, path), content)
	}
	if err := os.Chmod(filepath.Join(root, "src", "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/main.go", filepath.Join(root, "entry")); err != nil {
		t.Fatal(err)
	}
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return projectStore, root
}

func take(tb testing.TB, projectStore *store.Store, label string) store.Checkpoint {
	tb.Helper()
	checkpoint, err := projectStore.Take(store.CheckpointOptions{Label: label, Dir: projectStore.Root()})
	if err != nil {
		tb.Fatal(err)
	}
	return checkpoint
}

// apply restores checkpoint id, taking the safety checkpoint with a scan.
func apply(tb testing.TB, projectStore *store.Store, id int) Changes {
	tb.Helper()
	changes, err := Apply(projectStore, id, (*store.Store).Take)
	if err != nil {
		tb.Fatal(err)
	}
	return changes
}

// snapshot records the kind, permissions, modification time and content of
// every path under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	return testutil.Snapshot(t, root, testutil.SnapshotOptions{ShouldIncludeDirTimes: true})
}

func requireSame(t *testing.T, want, got map[string]string) {
	t.Helper()
	if maps.Equal(want, got) {
		return
	}
	var diff []string
	for path, wanted := range want {
		if found, ok := got[path]; !ok || found != wanted {
			diff = append(diff, fmt.Sprintf("%s: want %s, got %s", path, wanted, found))
		}
	}
	for path, found := range got {
		if _, ok := want[path]; !ok {
			diff = append(diff, fmt.Sprintf("%s: unexpected %s", path, found))
		}
	}
	t.Fatalf("restored tree differs:\n%s", strings.Join(diff, "\n"))
}

func restoreAfter(t *testing.T, damage func(root string)) {
	t.Helper()
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	take(t, projectStore, "before damage")

	damage(root)

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreRecursiveDelete(t *testing.T) {
	restoreAfter(t, func(root string) {
		if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestoreRecursiveDeleteOfGitDir(t *testing.T) {
	restoreAfter(t, func(root string) {
		if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestoreRootDeleted(t *testing.T) {
	restoreAfter(t, func(root string) {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestoreSedInPlace(t *testing.T) {
	restoreAfter(t, func(root string) {
		for _, path := range []string{"README.md", "src/main.go", "src/util/str.go"} {
			full := filepath.Join(root, path)
			data, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			testutil.WriteFile(t, full, strings.ReplaceAll(string(data), "package", "pkg"))
		}
	})
}

func TestRestoreMoveOverExisting(t *testing.T) {
	restoreAfter(t, func(root string) {
		if err := os.Rename(filepath.Join(root, "README.md"), filepath.Join(root, "src", "main.go")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestoreFileBecameDir(t *testing.T) {
	restoreAfter(t, func(root string) {
		if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "README.md", "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		testutil.WriteFile(t, filepath.Join(root, "README.md", "inner", "x"), "x")
		if err := os.RemoveAll(filepath.Join(root, "src", "util")); err != nil {
			t.Fatal(err)
		}
		testutil.WriteFile(t, filepath.Join(root, "src", "util"), "now a file")
	})
}

func TestRestoreKeepsNodeModules(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v1")
	take(t, projectStore, "before damage")

	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v2")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	if _, err := os.Stat(filepath.Join(root, "src", "main.go")); err != nil {
		t.Fatal("src/main.go not restored")
	}
	data, err := os.ReadFile(filepath.Join(root, "web", "node_modules", "pkg", "index.js"))
	if err != nil || string(data) != "v2" {
		t.Fatalf("excluded file changed by restore: %q, %v", data, err)
	}
}

func TestRestoreIsUndoable(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "before damage")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "written after the checkpoint")
	damaged := snapshot(t, root)

	apply(t, projectStore, 1)
	apply(t, projectStore, 2)
	requireSame(t, damaged, snapshot(t, root))
}

func TestPlanListsChanges(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "before damage")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "new")

	changes, err := Plan(projectStore, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changes.Written, "README.md") || !slices.Contains(changes.Removed, "new.txt") {
		t.Fatalf("Plan = %+v", changes)
	}
}

func TestRestoreDirEditedAfterBase(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main // edited\n")
	want := snapshot(t, root)
	take(t, projectStore, "after edit")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 2)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreDirKeepsEarlierDeletes(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	if err := os.Remove(filepath.Join(root, "src", "util", "str.go")); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	take(t, projectStore, "after delete")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 2)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreReadOnlyDirectory(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "ro", "f.txt"), "v1")
	if err := os.Chmod(filepath.Join(root, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "ro"), 0o755)
	want := snapshot(t, root)
	take(t, projectStore, "before")
	testutil.WriteFile(t, filepath.Join(root, "ro", "f.txt"), "v2 damaged")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterChmodReadOnly(t *testing.T) {
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	take(t, projectStore, "before")
	if err := os.Chmod(filepath.Join(root, "src", "util", "str.go"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "src", "main.go"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "src", "util"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "src"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(root, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "src", "util"), 0o755); err != nil {
			t.Fatal(err)
		}
	})

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreRecreatesNamedPipes(t *testing.T) {
	projectStore, root := newProject(t)
	if err := syscall.Mkfifo(filepath.Join(root, "src", "pipe"), 0o640); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	take(t, projectStore, "before")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreHardLinkedFiles(t *testing.T) {
	projectStore, root := newProject(t)
	if err := os.Link(filepath.Join(root, "README.md"), filepath.Join(root, "README.link")); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	take(t, projectStore, "before")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "damaged")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterPrune(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "v2")
	if err := os.Remove(filepath.Join(root, "src", "util", "str.go")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "newdir", "deep", "x"), "x")
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "v3")
	want := snapshot(t, root)
	take(t, projectStore, "3")
	if merged, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil || merged != 2 {
		t.Fatalf("Prune = %d, %v", merged, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 3)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreDirWhoseTimesChanged(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "src"), later, later); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	take(t, projectStore, "after touch")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 2)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreLeavesNewlyExcludedPaths(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "old build")
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "build/\n")
	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "new build")

	apply(t, projectStore, 1)
	if data, _ := os.ReadFile(filepath.Join(root, "build", "out.bin")); string(data) != "new build" {
		t.Fatalf("restore changed an excluded file: build/out.bin = %q", data)
	}
}

func TestRestoreLeavesUnreadableDirectories(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "pgdata", "rows"), "data")
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "pgdata"), 0o755)

	apply(t, projectStore, 1)
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "pgdata", "rows")); string(data) != "data" {
		t.Fatalf("restore removed a directory it could not read")
	}
}

func TestRestoreKeepsUnreadableFiles(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "secret.key"), "key")
	if err := os.Chmod(filepath.Join(root, "secret.key"), 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "secret.key"), 0o644)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "later.key"), "key made after the checkpoint")
	if err := os.Chmod(filepath.Join(root, "later.key"), 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "later.key"), 0o644)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "damaged")

	changes := apply(t, projectStore, 1)
	for _, name := range []string{"secret.key", "later.key"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Errorf("restore deleted %s, a file it could not read: %v (removed %v)", name, err, changes.Removed)
		}
	}
}

func TestRestoreLeavesDirectoryUnreadableAtCheckpoint(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "pgdata", "rows"), "data")
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "pgdata unreadable")
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o755); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	if data, _ := os.ReadFile(filepath.Join(root, "pgdata", "rows")); string(data) != "data" {
		t.Fatal("restore removed files the checkpoint could not read")
	}
}

func TestRestoreBringsBackPathsExcludedLater(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "src/\n")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	if data, _ := os.ReadFile(filepath.Join(root, "src", "main.go")); string(data) != "package main\n" {
		t.Fatal("restore did not bring back a folder excluded only after the checkpoint")
	}
}

func TestRestoreNeverWritesThroughSymlinkExcludedLater(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "keys", "authorized_keys"), "attacker key")
	take(t, projectStore, "first")
	outside := testutil.TempDir(t)
	if err := os.RemoveAll(filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "keys\n")

	apply(t, projectStore, 1)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("restore wrote %d entries outside the project through a symlink", len(entries))
	}
}

func TestRestoreKeepsChangedFileItCannotReadNow(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	secret := filepath.Join(root, "secret.key")
	testutil.WriteFile(t, secret, "v1")
	take(t, projectStore, "first")
	testutil.WriteFile(t, secret, "v2")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, secret, 0o600)

	changes := apply(t, projectStore, 1)
	if err := os.Chmod(secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(secret); string(data) != "v2" {
		t.Fatalf("secret.key = %q, want v2 left alone, since the restore could not save it first", data)
	}
	if !slices.Contains(changes.Kept, "secret.key") {
		t.Fatalf("Kept = %v, want secret.key", changes.Kept)
	}
}

func TestPlanRightAfterRestoreIsEmpty(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "changed")
	apply(t, projectStore, 1)

	changes, err := Plan(projectStore, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Written)+len(changes.Removed) != 0 {
		t.Fatalf("restoring checkpoint 1 again would write %v and remove %v", changes.Written, changes.Removed)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "changed again")
	if changes, err := Plan(projectStore, 1); err != nil || !slices.Equal(changes.Written, []string{"README.md"}) {
		t.Fatalf("after a later edit, Plan = %v, %v; want README.md written", changes.Written, err)
	}
}

func TestRestoreReplacesChangedNamedPipe(t *testing.T) {
	projectStore, root := newProject(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o640); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	take(t, projectStore, "first")
	if err := os.Chmod(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreKeepsNamedPipeAfterPrune(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	if err := syscall.Mkfifo(filepath.Join(root, "src", "pipe"), 0o640); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "with pipe")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "later")
	want := snapshot(t, root)
	take(t, projectStore, "later")
	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 3)
	requireSame(t, want, snapshot(t, root))
}

// runIn runs a command in dir and fails the test if it fails.
func runIn(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), name, args...)
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func newGitProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	projectStore, root := newProject(t)
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	runIn(t, root, "git", "init", "-q")
	runIn(t, root, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "add", "-A")
	runIn(t, root, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "initial")
	return projectStore, root
}

func TestRestoreAfterGitResetHard(t *testing.T) {
	projectStore, root := newGitProject(t)
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main // uncommitted work\n")
	want := snapshot(t, root)
	take(t, projectStore, "before reset")

	runIn(t, root, "git", "reset", "-q", "--hard")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterGitCleanFdx(t *testing.T) {
	projectStore, root := newGitProject(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "untracked notes\n")
	testutil.WriteFile(t, filepath.Join(root, "config.local.json"), "{}\n")
	want := snapshot(t, root)
	take(t, projectStore, "before clean")

	runIn(t, root, "git", "clean", "-q", "-fdx")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterXargsDeletion(t *testing.T) {
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	take(t, projectStore, "before xargs")

	runIn(t, root, "sh", "-c", "find . -name '*.go' -print0 | xargs -0 rm -f")

	apply(t, projectStore, 1)
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreReportsUnstableFiles(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	manifest, err := projectStore.Manifest(1)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Unstable = []string{"README.md"}
	var data bytes.Buffer
	if err := gob.NewEncoder(&data).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectStore.Dir(), "layers", "1", "manifest.gob"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "damaged")

	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Unstable, "README.md") {
		t.Fatalf("unstable = %v, want README.md reported", changes.Unstable)
	}
}

// BenchmarkRestore measures restoring 1,000 changed files in a project of
// 10,000 files.
func BenchmarkRestore(b *testing.B) {
	root := testutil.TempDir(b)
	for i := range 10_000 {
		testutil.WriteFile(b, filepath.Join(root, fmt.Sprintf("dir%03d", i%200), fmt.Sprintf("file%05d.go", i)), "package main")
	}
	projectStore, err := store.Create(b.TempDir(), root)
	if err != nil {
		b.Fatal(err)
	}
	take(b, projectStore, "first")
	for b.Loop() {
		b.StopTimer()
		for i := range 1_000 {
			testutil.WriteFile(b, filepath.Join(root, fmt.Sprintf("dir%03d", i%200), fmt.Sprintf("file%05d.go", i)), "changed")
		}
		b.StartTimer()
		apply(b, projectStore, 1)
	}
}

func TestRestoreNeverWritesOutsideWhenDirectoryIsSwappedForSymlink(t *testing.T) {
	projectStore, root := newProject(t)
	for i := range 20 {
		testutil.WriteFile(t, filepath.Join(root, "keys", fmt.Sprintf("key%d", i)), "saved")
	}
	take(t, projectStore, "first")
	for i := range 20 {
		testutil.WriteFile(t, filepath.Join(root, "keys", fmt.Sprintf("key%d", i)), "changed")
	}
	plan, unlock, err := lockAndPlan(projectStore, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	outside := testutil.TempDir(t)
	if err := os.Rename(filepath.Join(root, "keys"), filepath.Join(root, "keys-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "keys")); err != nil {
		t.Fatal(err)
	}

	if err := plan.apply(projectStore, 1); err == nil {
		t.Fatal("restore went on after a directory was replaced by a symlink")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("restore wrote %d entries outside the project", len(entries))
	}
}

// "cache" was a file at checkpoint 1. It is now a directory that holds only
// excluded content (node_modules), so the restore cannot remove it.
func TestRestoreKeepsDirectoryWithExcludedContentWhereFileWas(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "cache"), "file")
	take(t, projectStore, "first")
	if err := os.Remove(filepath.Join(root, "cache")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "cache", "node_modules", "x.js"), "x")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "second")

	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Kept, "cache") {
		t.Errorf("Kept = %v, want cache", changes.Kept)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "src", "main.go")); string(data) != "package main\n" {
		t.Errorf("src/main.go = %q; the restore stopped at cache", data)
	}
	if _, err := os.Stat(filepath.Join(root, "cache", "node_modules", "x.js")); err != nil {
		t.Errorf("the excluded content in cache is gone: %v", err)
	}
}

func TestRestoreReportsStoredFilesThatAreGone(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "# second")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package second\n")
	take(t, projectStore, "second")
	state, err := projectStore.State(2)
	if err != nil {
		t.Fatal(err)
	}
	// A power cut can lose the newest checkpoint's copies.
	if err := os.Remove(filepath.Join(projectStore.Dir(), "layers", "2", "files", "README.md")); err != nil {
		t.Fatal(err)
	}
	if state["README.md"].Layer != 2 {
		t.Fatalf("README.md is stored in layer %d, want 2", state["README.md"].Layer)
	}
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}

	changes := apply(t, projectStore, 2)
	if !slices.Equal(changes.Lost, []string{"README.md"}) {
		t.Fatalf("Lost = %v, want README.md", changes.Lost)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "src", "main.go")); string(data) != "package second\n" {
		t.Fatalf("src/main.go = %q; the restore stopped at the missing copy", data)
	}
}

// "cache" was a file at checkpoint 1. It is now a directory that holds a file
// undo cannot read, which the restore must keep, so the directory stays.
func TestRestoreKeepsDirectoryWithUnreadableFileWhereFileWas(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "cache"), "file")
	take(t, projectStore, "1")
	if err := os.Remove(filepath.Join(root, "cache")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "cache", "secret"), "s")
	if err := os.Chmod(filepath.Join(root, "cache", "secret"), 0); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "cache", "secret"), 0o644)
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "2")

	changes := apply(t, projectStore, 1)
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Errorf("README.md was not restored: %v", err)
	}
	if mainContent, err := os.ReadFile(filepath.Join(root, "src", "main.go")); err != nil || string(mainContent) != "package main\n" {
		t.Errorf("src/main.go = %q, %v; kept %v", mainContent, err, changes.Kept)
	}
}

// cache was a symlink at checkpoint 1. It is now a directory that holds
// excluded content, which the restore must keep.
func TestRestoreKeepsDirectoryWithExcludedContentWhereSymlinkWas(t *testing.T) {
	projectStore, root := newProject(t)
	if err := os.Symlink("src", filepath.Join(root, "cache")); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "1")
	if err := os.Remove(filepath.Join(root, "cache")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "cache", "node_modules", "x.js"), "x")
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "2")
	changes := apply(t, projectStore, 1)
	if mainContent, err := os.ReadFile(filepath.Join(root, "src", "main.go")); err != nil || string(mainContent) != "package main\n" {
		t.Errorf("src/main.go = %q, %v; kept %v", mainContent, err, changes.Kept)
	}
}

// replaceCacheFile takes checkpoint 1 with cache as a file, then makes cache
// a directory built by fill, damages other files and takes checkpoint 2.
func replaceCacheFile(t *testing.T, fill func(cache string)) (*store.Store, string) {
	t.Helper()
	projectStore, root := newProject(t)
	cache := filepath.Join(root, "cache")
	testutil.WriteFile(t, cache, "file")
	take(t, projectStore, "1")
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	fill(cache)
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "broken")
	take(t, projectStore, "2")
	return projectStore, root
}

// requireRestoredAroundCache restores checkpoint 1 and checks that the
// files around cache came back and cache was kept.
func requireRestoredAroundCache(t *testing.T, projectStore *store.Store, root string) {
	t.Helper()
	changes := apply(t, projectStore, 1)
	if !slices.Contains(changes.Kept, "cache") {
		t.Errorf("Kept = %v, want cache", changes.Kept)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(readme) != "# demo\n" {
		t.Errorf("README.md = %q, %v", readme, err)
	}
	mainContent, err := os.ReadFile(filepath.Join(root, "src", "main.go"))
	if err != nil || string(mainContent) != "package main\n" {
		t.Errorf("src/main.go = %q, %v", mainContent, err)
	}
}

func TestRestoreKeepsDirectoryWithNestedExcludedContentWhereFileWas(t *testing.T) {
	projectStore, root := replaceCacheFile(t, func(cache string) {
		testutil.WriteFile(t, filepath.Join(cache, "sub", "node_modules", "x.js"), "x")
	})
	requireRestoredAroundCache(t, projectStore, root)
}

func TestRestoreKeepsDirectoryWithNestedUnreadableContentWhereFileWas(t *testing.T) {
	testutil.SkipIfRoot(t)
	for name, unreadableRel := range map[string]string{"file": "sub/secret", "directory": "sub/unreadable", "itself": "."} {
		t.Run(name, func(t *testing.T) {
			var unreadable string
			projectStore, root := replaceCacheFile(t, func(cache string) {
				unreadable = filepath.Join(cache, filepath.FromSlash(unreadableRel))
				testutil.WriteFile(t, filepath.Join(cache, "sub", "unreadable", "f"), "s")
				testutil.WriteFile(t, filepath.Join(cache, "sub", "secret"), "s")
				if err := os.Chmod(unreadable, 0); err != nil {
					t.Fatal(err)
				}
				testutil.RestoreModeOnCleanup(t, unreadable, 0o755)
			})
			requireRestoredAroundCache(t, projectStore, root)
		})
	}
}

func TestRepeatedRestoreWritesOnlyWhatChangedSince(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	for _, rel := range []string{"README.md", "src/main.go", "src/util/str.go"} {
		testutil.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), "broken")
	}
	take(t, projectStore, "2")
	apply(t, projectStore, 1)

	for _, rel := range []string{"README.md", "src/main.go"} {
		testutil.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), "edited again")
		take(t, projectStore, "edit "+rel)
		if changes := apply(t, projectStore, 1); !slices.Equal(changes.Written, []string{rel}) {
			t.Fatalf("restore after editing %s wrote %v, want only %s", rel, changes.Written, rel)
		}
	}
}

func TestUndoRestoreOfDeletedProject(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	apply(t, projectStore, 1)
	safety, _, err := projectStore.Latest()
	if err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, safety.ID)
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("project = %v, %v, want an empty folder, as it was missing at the checkpoint", entries, err)
	}
}

func TestApplyWithChangesInManyDirectories(t *testing.T) {
	projectStore, root := newProject(t)
	writeInDirs := func(content string) {
		for i := range 1500 {
			testutil.WriteFile(t, filepath.Join(root, fmt.Sprintf("dir%04d", i), "file.txt"), content)
		}
	}
	writeInDirs("v1")
	first := take(t, projectStore, "1")
	writeInDirs("v2")
	take(t, projectStore, "2")
	writeInDirs("v3")
	testutil.SetOpenFileLimit(t, 1024)

	apply(t, projectStore, first.ID)
	if data, err := os.ReadFile(filepath.Join(root, "dir0042", "file.txt")); err != nil || string(data) != "v1" {
		t.Fatalf("dir0042/file.txt = %q, %v", data, err)
	}
}

func TestRestoreWithUnreadableFileTakesOneCheckpoint(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "secret.key"), "k")
	if err := os.Chmod(filepath.Join(root, "secret.key"), 0); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "secret.key"), 0o644)
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "edited")
	second := take(t, projectStore, "2")
	apply(t, projectStore, 1)
	latest, _, err := projectStore.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != second.ID+1 {
		t.Fatalf("the restore took %d checkpoints, want 1", latest.ID-second.ID)
	}
}

func TestApplyWithNothingToDoTakesNoCheckpoint(t *testing.T) {
	projectStore, _ := newProject(t)
	first := take(t, projectStore, "1")
	changes := apply(t, projectStore, first.ID)
	if len(changes.Written)+len(changes.Removed) > 0 {
		t.Fatalf("restoring the project's own state changed %+v", changes)
	}
	if latest, _, err := projectStore.Latest(); err != nil || latest.ID != first.ID {
		t.Fatalf("a restore with nothing to do took checkpoint %d, %v", latest.ID, err)
	}
}

// makeUnreadableThenTake makes rel unreadable after the restore plans and
// before its safety checkpoint, as a command running alongside it can.
func makeUnreadableThenTake(rel string) store.Checkpointer {
	return func(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		if err := os.Chmod(filepath.Join(projectStore.Root(), rel), 0); err != nil {
			return store.Checkpoint{}, err
		}
		return projectStore.Take(options)
	}
}

func readAsOwner(t *testing.T, path string) string {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestoreKeepsFileUnreadableAtSafetyCheckpoint(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "v1")
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "unsaved work")
	if _, err := Apply(projectStore, 1, makeUnreadableThenTake("notes.txt")); err != nil {
		t.Fatal(err)
	}
	if got := readAsOwner(t, filepath.Join(root, "notes.txt")); got != "unsaved work" {
		t.Fatalf("notes.txt holds %q: the restore overwrote work no checkpoint saved", got)
	}
}

func TestRestoreKeepsNewFileUnreadableAtSafetyCheckpoint(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "unsaved work")
	if _, err := Apply(projectStore, 1, makeUnreadableThenTake("new.txt")); err != nil {
		t.Fatal(err)
	}
	if got := readAsOwner(t, filepath.Join(root, "new.txt")); got != "unsaved work" {
		t.Fatalf("new.txt holds %q: the restore removed work no checkpoint saved", got)
	}
}

// The file is unreadable only while the safety checkpoint runs, and another
// change makes the restore plan again once it is readable.
func TestReplannedRestoreKeepsFileUnreadableAtSafetyCheckpoint(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t)
	notes := filepath.Join(root, "notes.txt")
	testutil.WriteFile(t, notes, "v1")
	take(t, projectStore, "1")
	testutil.WriteFile(t, notes, "unsaved work")
	checkpointer := func(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		testutil.WriteFile(t, filepath.Join(root, "other.txt"), "made during the restore")
		if err := os.Chmod(notes, 0); err != nil {
			return store.Checkpoint{}, err
		}
		defer os.Chmod(notes, 0o600) //nolint:errcheck // readAsOwner checks the mode again
		return projectStore.Take(options)
	}
	if _, err := Apply(projectStore, 1, checkpointer); err != nil {
		t.Fatal(err)
	}
	if readAsOwner(t, notes) == "unsaved work" {
		return
	}
	latest, _, err := projectStore.Latest()
	if err != nil {
		t.Fatal(err)
	}
	apply(t, projectStore, latest.ID)
	if got := readAsOwner(t, notes); got != "unsaved work" {
		t.Fatalf("after undoing the restore, notes.txt holds %q: the restore overwrote work no checkpoint saved", got)
	}
}

func TestPlanNamesExcludedPathsItCannotBringBack(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "x")
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "*.log\n")
	testutil.WriteFile(t, filepath.Join(root, "build.log"), "output")
	take(t, projectStore, "1")
	for _, rel := range []string{"node_modules", "web", "build.log"} {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := Plan(projectStore, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"build.log", "node_modules", "web/node_modules"}; !slices.Equal(changes.MissingExcluded, want) {
		t.Fatalf("missing excluded paths = %v, want %v", changes.MissingExcluded, want)
	}
	if !changes.Dirs["node_modules"] || changes.Dirs["build.log"] {
		t.Fatalf("dirs = %v, want node_modules marked and build.log not", changes.Dirs)
	}
}

// makeTemporaryFile creates and removes a file in dir, which changes only
// the folder's times.
func makeTemporaryFile(t *testing.T, dir string) {
	t.Helper()
	time.Sleep(10 * time.Millisecond)
	testutil.WriteFile(t, filepath.Join(dir, ".tmp"), "x")
	if err := os.Remove(filepath.Join(dir, ".tmp")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyWithOnlyFolderTimesChangedTakesNoCheckpoint(t *testing.T) {
	projectStore, root := newProject(t)
	first := take(t, projectStore, "1")
	makeTemporaryFile(t, root)
	makeTemporaryFile(t, filepath.Join(root, "src"))

	changes := apply(t, projectStore, first.ID)
	if len(changes.Written)+len(changes.Removed) != 0 {
		t.Fatalf("written %v, removed %v for folders whose only change is their times", changes.Written, changes.Removed)
	}
	if latest, _, err := projectStore.Latest(); err != nil || latest.ID != first.ID {
		t.Fatalf("a restore of folder times alone took checkpoint %d, %v", latest.ID, err)
	}
}

func TestRestoreSetsFolderTimesAlongWithRealChanges(t *testing.T) {
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	take(t, projectStore, "1")
	makeTemporaryFile(t, filepath.Join(root, "src"))
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "changed")

	changes := apply(t, projectStore, 1)
	if slices.Contains(changes.Written, "src") {
		t.Fatalf("written %v lists src, whose only change is its times", changes.Written)
	}
	requireSame(t, want, snapshot(t, root))
}

// allDefaultsRemoved is an .undoignore that brings back every default
// exclude, so a checkpoint is taken with no exclude patterns at all.
const allDefaultsRemoved = "!node_modules/\n!.venv/\n!venv/\n!__pycache__/\n!target/\n!.next/\n!.turbo/\n!.gradle/\n!.cache/\n!.undo-*/\n"

func TestRestoreUsesCheckpointRulesWhenTheyExcludedNothing(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), allDefaultsRemoved)
	testutil.WriteFile(t, filepath.Join(root, "target", "release.bin"), "built by hand")
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), strings.Replace(allDefaultsRemoved, "!target/\n", "", 1))
	if err := os.RemoveAll(filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, 1)
	assertContent(t, filepath.Join(root, "target", "release.bin"), "built by hand")
}
