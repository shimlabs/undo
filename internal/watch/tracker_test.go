package watch

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

func newTracker(t *testing.T) (*Tracker, *store.Store, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"README.md":       "# demo",
		"src/main.go":     "package main",
		"src/lib/util.go": "package lib",
		"docs/guide.md":   "guide",
	} {
		writeFile(t, filepath.Join(root, path), content)
	}
	return startTracker(t, root)
}

func startTracker(t *testing.T, root string) (*Tracker, *store.Store, string) {
	t.Helper()
	s, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := NewTracker(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker.Close)
	return tracker, s, root
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

func fingerprint(t *testing.T, root string) map[string]string {
	t.Helper()
	prints := map[string]string{}
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, _ := os.ReadFile(path)
		prints[rel] = fmt.Sprintf("%v %x", entry.IsDir(), sha256.Sum256(data))
		return nil
	})
	return prints
}

func damage(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "README.md"), "# changed")
	os.RemoveAll(filepath.Join(root, "src"))
	writeFile(t, filepath.Join(root, "new", "deep", "file.txt"), "new")
	os.Rename(filepath.Join(root, "docs", "guide.md"), filepath.Join(root, "docs", "moved.md"))
}

func checkpoint(t *testing.T, tracker *Tracker, label string) store.Checkpoint {
	t.Helper()
	cp, err := tracker.Checkpoint(store.CheckpointOptions{Label: label, Dir: tracker.root})
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

func checkMatchesScan(t *testing.T, s *store.Store, root string, id int) {
	t.Helper()
	matcher, _ := exclude.New(root)
	scanned, err := tree.Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.ListingAt(id)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(scanned, stored) {
		changed, deleted := tree.Changed(stored, scanned)
		t.Fatalf("checkpoint %d differs from a scan: changed %v, deleted %v", id, changed, deleted)
	}
}

func checkpointThroughTracker(tracker *Tracker) store.Checkpointer {
	return func(_ *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		return tracker.Checkpoint(options)
	}
}

func TestTrackerMatchesFullScan(t *testing.T) {
	tracker, s, root := newTracker(t)
	checkpoint(t, tracker, "first")
	damage(t, root)

	cp := checkpoint(t, tracker, "second")

	checkMatchesScan(t, s, root, cp.ID)
}

func TestTrackerCheckpointsRestoreExactly(t *testing.T) {
	tracker, s, root := newTracker(t)
	before := fingerprint(t, root)
	checkpoint(t, tracker, "first")
	checkpoint(t, tracker, "second")
	damage(t, root)
	checkpoint(t, tracker, "third")

	if _, err := restore.Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatalf("restore through tracker layers differs:\nbefore %v\nafter  %v", before, after)
	}
}

func TestTrackerSeesChangeRightBeforeCheckpoint(t *testing.T) {
	tracker, s, root := newTracker(t)
	checkpoint(t, tracker, "first")
	checkpoint(t, tracker, "warm")

	for i := range 20 {
		name := fmt.Sprintf("f%d.txt", i)
		writeFile(t, filepath.Join(root, name), "x")
		cp := checkpoint(t, tracker, "after write")
		manifest, _ := s.Manifest(cp.ID)
		if _, ok := manifest.Changed[name]; !ok {
			t.Fatalf("round %d: checkpoint missed %s written just before it", i, name)
		}
	}
}

func TestTrackerRecoversAfterOutsideCheckpoint(t *testing.T) {
	tracker, s, root := newTracker(t)
	before := fingerprint(t, root)
	checkpoint(t, tracker, "first")
	checkpoint(t, tracker, "second")

	writeFile(t, filepath.Join(root, "README.md"), "# outside")
	if _, err := s.Take(store.CheckpointOptions{Label: "taken without the tracker", Dir: root}); err != nil {
		t.Fatal(err)
	}
	damage(t, root)
	checkpoint(t, tracker, "after outside")

	if _, err := restore.Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatal("restore after an outside checkpoint is not exact")
	}
}

func TestTrackerFollowsMovedDirectories(t *testing.T) {
	tracker, s, root := newTracker(t)
	checkpoint(t, tracker, "first")

	os.Rename(filepath.Join(root, "src"), filepath.Join(root, "moved"))
	checkpoint(t, tracker, "after move")
	writeFile(t, filepath.Join(root, "moved", "lib", "util.go"), "package lib // edited")
	writeFile(t, filepath.Join(root, "moved", "lib", "new.go"), "package lib")

	cp := checkpoint(t, tracker, "after edit")
	checkMatchesScan(t, s, root, cp.ID)
}

func TestTrackerSeesRecreatedProject(t *testing.T) {
	tracker, s, root := newTracker(t)
	checkpoint(t, tracker, "first")

	os.RemoveAll(root)
	writeFile(t, filepath.Join(root, "README.md"), "# recreated")
	checkpoint(t, tracker, "after recreate")
	writeFile(t, filepath.Join(root, "README.md"), "# edited again")
	writeFile(t, filepath.Join(root, "src", "main.go"), "package main")

	cp := checkpoint(t, tracker, "after edit")
	checkMatchesScan(t, s, root, cp.ID)
}

func TestTrackerFollowsExcludeRuleChanges(t *testing.T) {
	tracker, s, root := newTracker(t)
	writeFile(t, filepath.Join(root, ".undoignore"), "data\n")
	writeFile(t, filepath.Join(root, "data", "important.db"), "precious")
	checkpoint(t, tracker, "data excluded")

	writeFile(t, filepath.Join(root, ".undoignore"), "# protect data now\n")
	cp := checkpoint(t, tracker, "data included")
	checkMatchesScan(t, s, root, cp.ID)

	os.Remove(filepath.Join(root, "README.md"))
	if _, err := restore.Apply(s, cp.ID, checkpointThroughTracker(tracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "data", "important.db")); string(data) != "precious" {
		t.Fatalf("data/important.db = %q after the restore", data)
	}
}

func TestTrackerAppliesDirectoryPatterns(t *testing.T) {
	tracker, s, root := newTracker(t)
	writeFile(t, filepath.Join(root, ".undoignore"), "build/\n")
	checkpoint(t, tracker, "first")

	writeFile(t, filepath.Join(root, "build", "out.bin"), "old build")
	cp := checkpoint(t, tracker, "second")
	state, _ := s.State(cp.ID)
	if _, ok := state["build/out.bin"]; ok {
		t.Fatal("the tracker recorded a path that build/ excludes")
	}

	writeFile(t, filepath.Join(root, "build", "out.bin"), "new build")
	os.Remove(filepath.Join(root, "README.md"))
	if _, err := restore.Apply(s, cp.ID, checkpointThroughTracker(tracker)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "build", "out.bin")); string(data) != "new build" {
		t.Fatalf("restore changed an excluded file: build/out.bin = %q", data)
	}
}

func TestTrackerWithRootTypedInOtherCase(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	stored := filepath.Join(parent, "MyProj")
	writeFile(t, filepath.Join(stored, "a.txt"), "v1")
	typed := filepath.Join(parent, strings.ToLower("MyProj"))
	if _, err := os.Stat(typed); err != nil {
		t.Skip("the filesystem is case-sensitive")
	}
	root, err := project.Root(typed)
	if err != nil {
		t.Fatal(err)
	}
	tracker, s, _ := startTracker(t, root)
	checkpoint(t, tracker, "first")

	writeFile(t, filepath.Join(stored, "a.txt"), "v2 edited")
	cp := checkpoint(t, tracker, "second")

	checkMatchesScan(t, s, stored, cp.ID)
}
