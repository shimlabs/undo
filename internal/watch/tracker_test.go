package watch

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/tree"
)

func newTracked(t *testing.T) (*Tracker, *store.Store, string) {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
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
		write(t, filepath.Join(root, path), content)
	}
	s, err := store.Open(root)
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

func write(t *testing.T, path, content string) {
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
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, _ := os.ReadFile(path)
		prints[rel] = fmt.Sprintf("%v %x", d.IsDir(), sha256.Sum256(data))
		return nil
	})
	return prints
}

func damage(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, "README.md"), "# changed")
	os.RemoveAll(filepath.Join(root, "src"))
	write(t, filepath.Join(root, "new", "deep", "file.txt"), "new")
	os.Rename(filepath.Join(root, "docs", "guide.md"), filepath.Join(root, "docs", "moved.md"))
}

func TestTrackerMatchesFullScan(t *testing.T) {
	tracker, s, root := newTracked(t)
	if _, err := tracker.Checkpoint("first", root); err != nil {
		t.Fatal(err)
	}
	damage(t, root)

	cp, err := tracker.Checkpoint("second", root)
	if err != nil {
		t.Fatal(err)
	}

	ex, _ := exclude.New(root)
	scanned, err := tree.Scan(root, ex)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.ListingAt(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(scanned, stored) {
		changed, deleted := tree.Changed(stored, scanned)
		t.Fatalf("tracker listing differs from a scan: changed %v, deleted %v", changed, deleted)
	}
}

func TestTrackerCheckpointsRestoreExactly(t *testing.T) {
	tracker, s, root := newTracked(t)
	before := fingerprint(t, root)
	tracker.Checkpoint("first", root)
	tracker.Checkpoint("second", root)
	damage(t, root)
	tracker.Checkpoint("third", root)

	if _, err := restore.Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatalf("restore through tracker layers differs:\nbefore %v\nafter  %v", before, after)
	}
}

func TestTrackerSeesChangeRightBeforeCheckpoint(t *testing.T) {
	tracker, s, root := newTracked(t)
	tracker.Checkpoint("first", root)
	tracker.Checkpoint("warm", root)

	for i := range 20 {
		name := fmt.Sprintf("f%d.txt", i)
		write(t, filepath.Join(root, name), "x")
		cp, err := tracker.Checkpoint("after write", root)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := s.Layer(cp.ID)
		if _, ok := m.Changed[name]; !ok {
			t.Fatalf("round %d: checkpoint missed %s written just before it", i, name)
		}
	}
}

func TestTrackerRecoversAfterOutsideCheckpoint(t *testing.T) {
	tracker, s, root := newTracked(t)
	before := fingerprint(t, root)
	tracker.Checkpoint("first", root)
	tracker.Checkpoint("second", root)

	write(t, filepath.Join(root, "README.md"), "# outside")
	if _, err := s.Take("taken without the tracker", root); err != nil {
		t.Fatal(err)
	}
	damage(t, root)
	if _, err := tracker.Checkpoint("after outside", root); err != nil {
		t.Fatal(err)
	}

	if _, err := restore.Apply(s, 2, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(t, root); !maps.Equal(before, after) {
		t.Fatal("restore after an outside checkpoint is not exact")
	}
}

func checkMatchesScan(t *testing.T, s *store.Store, root string, id int) {
	t.Helper()
	ex, _ := exclude.New(root)
	scanned, err := tree.Scan(root, ex)
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

func TestTrackerFollowsMovedDirectories(t *testing.T) {
	tracker, s, root := newTracked(t)
	tracker.Checkpoint("first", root)

	os.Rename(filepath.Join(root, "src"), filepath.Join(root, "moved"))
	tracker.Checkpoint("after move", root)
	write(t, filepath.Join(root, "moved", "lib", "util.go"), "package lib // edited")
	write(t, filepath.Join(root, "moved", "lib", "new.go"), "package lib")

	cp, err := tracker.Checkpoint("after edit", root)
	if err != nil {
		t.Fatal(err)
	}
	checkMatchesScan(t, s, root, cp.ID)
}

func TestTrackerSeesRecreatedProject(t *testing.T) {
	tracker, s, root := newTracked(t)
	tracker.Checkpoint("first", root)

	os.RemoveAll(root)
	write(t, filepath.Join(root, "README.md"), "# recreated")
	tracker.Checkpoint("after recreate", root)
	write(t, filepath.Join(root, "README.md"), "# edited again")
	write(t, filepath.Join(root, "src", "main.go"), "package main")

	cp, err := tracker.Checkpoint("after edit", root)
	if err != nil {
		t.Fatal(err)
	}
	checkMatchesScan(t, s, root, cp.ID)
}
