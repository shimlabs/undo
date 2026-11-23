package restore

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"

	"github.com/shimlabs/undo/internal/store"
)

func newProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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

func take(t *testing.T, projectStore *store.Store, label string) {
	t.Helper()
	if _, err := projectStore.Take(store.CheckpointOptions{Label: label, Dir: projectStore.Root()}); err != nil {
		t.Fatal(err)
	}
}

// snapshot records the kind, permissions, modification time and content of
// every path under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			entries[rel] = "link " + target
		case info.Mode()&fs.ModeNamedPipe != 0:
			entries[rel] = fmt.Sprintf("fifo %v", info.Mode().Perm())
		case info.IsDir():
			entries[rel] = fmt.Sprintf("dir %v %d", info.Mode().Perm(), info.ModTime().UnixNano())
		default:
			data, _ := os.ReadFile(path)
			entries[rel] = fmt.Sprintf("file %v %d %q", info.Mode().Perm(), info.ModTime().UnixNano(), data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
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
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before damage", Dir: root}); err != nil {
		t.Fatal(err)
	}

	damage(root)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreRecursiveDelete(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(filepath.Join(root, "src"))
	})
}

func TestRestoreRecursiveDeleteOfGitDir(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(filepath.Join(root, ".git"))
	})
}

func TestRestoreRootDeleted(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.RemoveAll(root)
	})
}

func TestRestoreSedInPlace(t *testing.T) {
	restoreAfter(t, func(root string) {
		for _, path := range []string{"README.md", "src/main.go", "src/util/str.go"} {
			full := filepath.Join(root, path)
			data, _ := os.ReadFile(full)
			os.WriteFile(full, []byte(strings.ReplaceAll(string(data), "package", "pkg")), 0o644)
		}
	})
}

func TestRestoreMoveOverExisting(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.Rename(filepath.Join(root, "README.md"), filepath.Join(root, "src", "main.go"))
	})
}

func TestRestoreFileBecameDir(t *testing.T) {
	restoreAfter(t, func(root string) {
		os.Remove(filepath.Join(root, "README.md"))
		os.MkdirAll(filepath.Join(root, "README.md", "inner"), 0o755)
		os.WriteFile(filepath.Join(root, "README.md", "inner", "x"), []byte("x"), 0o644)
		os.RemoveAll(filepath.Join(root, "src", "util"))
		os.WriteFile(filepath.Join(root, "src", "util"), []byte("now a file"), 0o644)
	})
}

func TestRestoreKeepsNodeModules(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v1")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before damage", Dir: root}); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), "v2")
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
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
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before damage", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "written after the checkpoint")
	damaged := snapshot(t, root)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, damaged, snapshot(t, root))
}

func TestPlanListsChanges(t *testing.T) {
	projectStore, root := newProject(t)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before damage", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "README.md"))
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
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "package main // edited\n")
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "after edit", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreDirKeepsEarlierDeletes(t *testing.T) {
	projectStore, root := newProject(t)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "src", "util", "str.go"))
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "after delete", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreReadOnlyDirectory(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "ro", "f.txt"), "v1")
	os.Chmod(filepath.Join(root, "ro"), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "ro"), 0o755) })
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "ro", "f.txt"), []byte("v2 damaged"), 0o644)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterChmodReadOnly(t *testing.T) {
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(root, "src", "util", "str.go"), 0o444)
	os.Chmod(filepath.Join(root, "src", "main.go"), 0o555)
	os.Chmod(filepath.Join(root, "src", "util"), 0o555)
	os.Chmod(filepath.Join(root, "src"), 0o555)
	t.Cleanup(func() {
		os.Chmod(filepath.Join(root, "src"), 0o755)
		os.Chmod(filepath.Join(root, "src", "util"), 0o755)
	})

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreRecreatesNamedPipes(t *testing.T) {
	projectStore, root := newProject(t)
	if err := syscall.Mkfifo(filepath.Join(root, "src", "pipe"), 0o640); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreHardLinkedFiles(t *testing.T) {
	projectStore, root := newProject(t)
	os.Link(filepath.Join(root, "README.md"), filepath.Join(root, "README.link"))
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "before", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "README.md"), []byte("damaged"), 0o644)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterPrune(t *testing.T) {
	projectStore, root := newProject(t)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "1", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "src", "main.go"), "v2")
	os.Remove(filepath.Join(root, "src", "util", "str.go"))
	testutil.WriteFile(t, filepath.Join(root, "newdir", "deep", "x"), "x")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "2", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "v3")
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "3", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if merged, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil || merged != 2 {
		t.Fatalf("Prune = %d, %v", merged, err)
	}
	os.RemoveAll(root)

	if _, err := Apply(projectStore, 3, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreDirWhoseTimesChanged(t *testing.T) {
	projectStore, root := newProject(t)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(root, "src"), later, later)
	want := snapshot(t, root)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "after touch", Dir: root}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreLeavesNewlyExcludedPaths(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "old build")
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "build/\n")
	testutil.WriteFile(t, filepath.Join(root, "build", "out.bin"), "new build")

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "build", "out.bin")); string(data) != "new build" {
		t.Fatalf("restore changed an excluded file: build/out.bin = %q", data)
	}
}

func TestRestoreLeavesUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	projectStore, root := newProject(t)
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "pgdata", "rows"), "data")
	os.Chmod(filepath.Join(root, "pgdata"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(root, "pgdata"), 0o755)
	if data, _ := os.ReadFile(filepath.Join(root, "pgdata", "rows")); string(data) != "data" {
		t.Fatalf("restore removed a directory it could not read")
	}
}

func TestRestoreKeepsUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every file")
	}
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "secret.key"), "key")
	os.Chmod(filepath.Join(root, "secret.key"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "secret.key"), 0o644) })
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, "later.key"), "key made after the checkpoint")
	os.Chmod(filepath.Join(root, "later.key"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "later.key"), 0o644) })
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "damaged")

	changes, err := Apply(projectStore, 1, (*store.Store).Take)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secret.key", "later.key"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Errorf("restore deleted %s, a file it could not read: %v (removed %v)", name, err, changes.Removed)
		}
	}
}

func TestRestoreLeavesDirectoryUnreadableAtCheckpoint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "pgdata", "rows"), "data")
	os.Chmod(filepath.Join(root, "pgdata"), 0o000)
	take(t, projectStore, "pgdata unreadable")
	os.Chmod(filepath.Join(root, "pgdata"), 0o755)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "pgdata", "rows")); string(data) != "data" {
		t.Fatal("restore removed files the checkpoint could not read")
	}
}

func TestRestoreBringsBackPathsExcludedLater(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "src/\n")
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "src", "main.go")); string(data) != "package main\n" {
		t.Fatal("restore did not bring back a folder excluded only after the checkpoint")
	}
}

func TestRestoreReplacesChangedNamedPipe(t *testing.T) {
	projectStore, root := newProject(t)
	syscall.Mkfifo(filepath.Join(root, "pipe"), 0o640)
	want := snapshot(t, root)
	take(t, projectStore, "first")
	os.Chmod(filepath.Join(root, "pipe"), 0o600)

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreKeepsNamedPipeAfterPrune(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	syscall.Mkfifo(filepath.Join(root, "src", "pipe"), 0o640)
	take(t, projectStore, "with pipe")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "later")
	want := snapshot(t, root)
	take(t, projectStore, "later")
	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, "src"))

	if _, err := Apply(projectStore, 3, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

// runIn runs a command in dir and fails the test if it fails.
func runIn(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
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
	os.RemoveAll(filepath.Join(root, ".git"))
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

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterGitCleanFdx(t *testing.T) {
	projectStore, root := newGitProject(t)
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "untracked notes\n")
	testutil.WriteFile(t, filepath.Join(root, "config.local.json"), "{}\n")
	want := snapshot(t, root)
	take(t, projectStore, "before clean")

	runIn(t, root, "git", "clean", "-q", "-fdx")

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreAfterXargsDeletion(t *testing.T) {
	projectStore, root := newProject(t)
	want := snapshot(t, root)
	take(t, projectStore, "before xargs")

	runIn(t, root, "sh", "-c", "find . -name '*.go' -print0 | xargs -0 rm -f")

	if _, err := Apply(projectStore, 1, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	requireSame(t, want, snapshot(t, root))
}

func TestRestoreReportsUnstableFiles(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "first")
	manifest, _ := projectStore.Manifest(1)
	manifest.Unstable = []string{"README.md"}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(projectStore.Dir(), "layers", "1", "manifest.json"), data, 0o600)
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "damaged")

	changes, err := Apply(projectStore, 1, (*store.Store).Take)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changes.Unstable, "README.md") {
		t.Fatalf("unstable = %v, want README.md reported", changes.Unstable)
	}
}
