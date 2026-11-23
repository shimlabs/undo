package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/tree"
)

func newProject(t *testing.T, files map[string]string) (*Store, string) {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		writeFile(t, filepath.Join(root, path), content)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFirstTakeClonesBase(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha"})

	cp, err := s.Take(CheckpointOptions{Label: "first", Dir: root})
	if err != nil {
		t.Fatal(err)
	}

	if cp.ID != 1 {
		t.Fatalf("first checkpoint id = %d, want 1", cp.ID)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "base", "a.txt")); got != "alpha" {
		t.Fatalf("base a.txt = %q", got)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Label != "first" || list[0].Dir != root {
		t.Fatalf("List() = %+v", list)
	}
}

func TestTakeRecordsChangedAndDeleted(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	if _, err := s.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, "a.txt"), "alpha two")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "c", "d.txt"), "delta")

	cp, err := s.Take(CheckpointOptions{Label: "second", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := s.Manifest(cp.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"a.txt", "c", "c/d.txt"} {
		if _, ok := manifest.Changed[path]; !ok {
			t.Errorf("manifest missing changed path %q", path)
		}
	}
	if !slices.Equal(manifest.Deleted, []string{"b.txt"}) {
		t.Errorf("deleted = %v, want [b.txt]", manifest.Deleted)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "layers", "2", "files", "a.txt")); got != "alpha two" {
		t.Errorf("layer a.txt = %q", got)
	}
}

func TestTakeIgnoresExcluded(t *testing.T) {
	s, root := newProject(t, map[string]string{"node_modules/pkg/index.js": "v1", "main.js": "x"})
	if _, err := s.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "base", "node_modules")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("node_modules was cloned into the base")
	}

	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "v2")
	cp, err := s.Take(CheckpointOptions{Label: "second", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := s.Manifest(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changed) != 0 || len(manifest.Deleted) != 0 {
		t.Fatalf("excluded change recorded: %+v", manifest)
	}
}

func TestTakeLockedConcurrently(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha"})
	if _, err := s.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	ids := make([]int, 2)
	for i := range ids {
		wg.Go(func() {
			other, err := Open(root)
			if err != nil {
				t.Error(err)
				return
			}
			cp, err := other.Take(CheckpointOptions{Label: "parallel", Dir: root})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = cp.ID
		})
	}
	wg.Wait()

	slices.Sort(ids)
	if !slices.Equal(ids, []int{2, 3}) {
		t.Fatalf("ids = %v, want [2 3]", ids)
	}
}

func TestFirstTakeNeverReadsExcluded(t *testing.T) {
	s, root := newProject(t, map[string]string{"main.js": "x", "lib/util.js": "u"})
	locked := filepath.Join(root, "node_modules", "locked")
	writeFile(t, filepath.Join(locked, "secret.js"), "s")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	if _, err := s.Take(CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatalf("first checkpoint read an excluded directory: %v", err)
	}
	if got := readFile(t, filepath.Join(s.Dir(), "base", "lib", "util.js")); got != "u" {
		t.Fatalf("base lib/util.js = %q", got)
	}
}

func TestListingAtRebuildsWhenSavedCopyIsMissing(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	s.Take(CheckpointOptions{Label: "first", Dir: root})
	writeFile(t, filepath.Join(root, "a.txt"), "alpha two")
	os.Remove(filepath.Join(root, "b.txt"))
	cp, err := s.Take(CheckpointOptions{Label: "second", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.ListingAt(cp.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(s.Dir(), "listing.gob")); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.ListingAt(cp.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(rebuilt) != len(saved) {
		t.Fatalf("rebuilt %d paths, saved %d", len(rebuilt), len(saved))
	}
	for rel, entry := range saved {
		if rebuilt[rel] != entry {
			t.Fatalf("%s: rebuilt %+v, saved %+v", rel, rebuilt[rel], entry)
		}
	}
}

func TestCommitRecordsGivenChanges(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	first, _ := s.Take(CheckpointOptions{Label: "first", Dir: root})
	writeFile(t, filepath.Join(root, "a.txt"), "alpha two")
	os.Remove(filepath.Join(root, "b.txt"))

	current, err := s.ListingAt(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := tree.Stat(filepath.Join(root, "a.txt"))
	current["a.txt"] = entry
	delete(current, "b.txt")

	cp, err := s.Commit(first.ID, CheckpointOptions{Label: "from watcher", Dir: root}, Changes{Listing: current, Changed: []string{"a.txt"}, Deleted: []string{"b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := s.Manifest(cp.ID)
	if _, ok := manifest.Changed["a.txt"]; !ok || !slices.Equal(manifest.Deleted, []string{"b.txt"}) {
		t.Fatalf("manifest = %+v", manifest)
	}
	if got := readFile(t, s.layerFile(cp.ID, "a.txt")); got != "alpha two" {
		t.Fatalf("layer a.txt = %q", got)
	}
}

func TestCommitRefusesStaleChanges(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "alpha"})
	first, _ := s.Take(CheckpointOptions{Label: "first", Dir: root})
	s.Take(CheckpointOptions{Label: "second", Dir: root})
	current, _ := s.ListingAt(first.ID)

	if _, err := s.Commit(first.ID, CheckpointOptions{Label: "stale", Dir: root}, Changes{Listing: current}); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestTakeSkipsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	s, root := newProject(t, map[string]string{"a.txt": "a", "pgdata/base/1": "rows"})
	os.Chmod(filepath.Join(root, "pgdata"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })

	cp, err := s.Take(CheckpointOptions{Label: "first", Dir: root})
	if err != nil {
		t.Fatalf("one unreadable directory failed the checkpoint: %v", err)
	}
	manifest, _ := s.Manifest(cp.ID)
	if !slices.Equal(manifest.Unreadable, []string{"pgdata"}) {
		t.Fatalf("unreadable = %v, want [pgdata]", manifest.Unreadable)
	}
}

func TestTakeProtectsFilesNamedLikeExcludedDirectories(t *testing.T) {
	s, root := newProject(t, map[string]string{"target": "important", ".cache": "x"})

	cp, err := s.Take(CheckpointOptions{Label: "first", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.State(cp.ID)
	if _, ok := state["target"]; !ok {
		t.Fatal("a regular file named target is not protected")
	}
}

func TestTakeShortensLongLabels(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "a"})
	heredoc := "cat > big.txt <<EOF\n" + strings.Repeat("line\n", 1000) + "EOF"

	cp, err := s.Take(CheckpointOptions{Label: heredoc, Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Label != "cat > big.txt <<EOF …" {
		t.Fatalf("label = %q", cp.Label)
	}
	if long := strings.Repeat("x", 500); shortLabel(long) != strings.Repeat("x", maxLabelLength)+" …" {
		t.Fatal("a long single-line label was not cut")
	}
}

func TestTakeClearsLeftoverLayer(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "a"})
	s.Take(CheckpointOptions{Label: "first", Dir: root})
	writeFile(t, s.layerFile(2, "stale.txt"), "left by a crash")
	writeFile(t, filepath.Join(root, "a.txt"), "a2")

	cp, err := s.Take(CheckpointOptions{Label: "second", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.layerFile(cp.ID, "stale.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a file from an interrupted checkpoint was kept in the new layer")
	}
}

func TestLatestIgnoresLineCutOffByCrash(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "a"})
	first, _ := s.Take(CheckpointOptions{Label: "first", Dir: root})
	file, _ := os.OpenFile(s.indexPath(), os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(`{"id":2,"time":"2026-`)
	file.Close()

	latest, ok, err := s.Latest()
	if err != nil || !ok || latest.ID != first.ID {
		t.Fatalf("Latest = %+v, %v, %v; want checkpoint %d", latest, ok, err, first.ID)
	}
	if list, err := s.List(); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
}

func TestMergeIntoReadOnlyDirectory(t *testing.T) {
	s, root := newProject(t, map[string]string{"ro/f.txt": "v1"})
	os.Chmod(filepath.Join(root, "ro"), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "ro"), 0o755) })
	for _, content := range []string{"v1", "v2", "v3"} {
		os.WriteFile(filepath.Join(root, "ro", "f.txt"), []byte(content), 0o644)
		if _, err := s.Take(CheckpointOptions{Label: content, Dir: root}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Prune(time.Now(), Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("Prune into a read-only directory: %v", err)
	}
	os.WriteFile(filepath.Join(root, "ro", "f.txt"), []byte("v4"), 0o644)
	if _, err := s.Take(CheckpointOptions{Label: "v4", Dir: root}); err != nil {
		t.Fatalf("checkpoint after the merge: %v", err)
	}
}

func TestFailedMergeKeepsCheckpointsWorking(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "a"})
	s.Take(CheckpointOptions{Label: "first", Dir: root})
	if err := writeJSON(s.mergePath(), pendingMerge{Through: 1}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(s.layerDir(1), "manifest.json"))

	writeFile(t, filepath.Join(root, "a.txt"), "a2")
	if _, err := s.Take(CheckpointOptions{Label: "second", Dir: root}); err != nil {
		t.Fatalf("a failing merge stopped checkpoints: %v", err)
	}
	if _, err := s.State(2); !errors.Is(err, ErrMergePending) {
		t.Fatalf("State = %v, want ErrMergePending", err)
	}
	if !strings.Contains(s.LastError(), "merge") {
		t.Fatalf("the merge failure was not logged: %q", s.LastError())
	}
}

func TestPruneIfDueRunsHourly(t *testing.T) {
	s, root := newProject(t, map[string]string{"a.txt": "a"})
	for i := range 3 {
		writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("a", i+2))
		s.Take(CheckpointOptions{Label: "edit", Dir: root})
	}
	keepOne := Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	now := time.Now()

	if merged, err := s.PruneIfDue(now, Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 1 << 40}); err != nil || merged != 0 {
		t.Fatalf("first PruneIfDue = %d, %v", merged, err)
	}
	if merged, _ := s.PruneIfDue(now.Add(time.Minute), keepOne); merged != 0 {
		t.Fatalf("PruneIfDue pruned again within the hour: merged %d", merged)
	}
	if merged, _ := s.PruneIfDue(now.Add(2*time.Hour), keepOne); merged != 2 {
		t.Fatalf("PruneIfDue after an hour merged %d, want 2", merged)
	}
}
