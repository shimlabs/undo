package store_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
)

func newProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	s, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fingerprint(root string) map[string]string {
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

// history takes four checkpoints with different files and returns the
// project's fingerprint at each of them.
func history(t *testing.T, s *store.Store, root string) []map[string]string {
	t.Helper()
	var states []map[string]string
	steps := []func(){
		func() { writeFile(t, filepath.Join(root, "a.txt"), []byte("one")) },
		func() { writeFile(t, filepath.Join(root, "dir", "b.txt"), []byte("two")) },
		func() { os.Remove(filepath.Join(root, "a.txt")) },
		func() {
			os.RemoveAll(filepath.Join(root, "dir"))
			writeFile(t, filepath.Join(root, "dir"), []byte("now a file"))
		},
	}
	for i, step := range steps {
		step()
		states = append(states, fingerprint(root))
		if _, err := s.Take(store.CheckpointOptions{Label: fmt.Sprintf("step %d", i+1), Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	return states
}

func TestPruneMergesOldestIntoBase(t *testing.T) {
	s, root := newProject(t)
	states := history(t, s, root)

	merged, err := s.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 2, MaxBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if merged != 2 {
		t.Fatalf("merged %d checkpoints, want 2", merged)
	}
	list, _ := s.List()
	if len(list) != 2 || list[0].ID != 3 || list[1].ID != 4 {
		t.Fatalf("remaining checkpoints = %+v", list)
	}
	for _, id := range []int{3, 4} {
		if _, err := restore.Apply(s, id, (*store.Store).Take); err != nil {
			t.Fatal(err)
		}
		if got := fingerprint(root); !maps.Equal(got, states[id-1]) {
			t.Fatalf("restore to %d after pruning:\n got %v\nwant %v", id, got, states[id-1])
		}
	}
}

func TestPruneByAgeKeepsLatest(t *testing.T) {
	s, root := newProject(t)
	history(t, s, root)

	if _, err := s.Prune(time.Now().Add(8*24*time.Hour), store.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].ID != 4 {
		t.Fatalf("remaining checkpoints = %+v, want only 4", list)
	}
}

func TestUsageCountsOnlyUnsharedData(t *testing.T) {
	s, root := newProject(t)
	data := bytes.Repeat([]byte("x"), 4<<20)
	writeFile(t, filepath.Join(root, "big.bin"), data)
	if _, err := s.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}

	shared, err := s.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if shared > 1<<20 {
		t.Fatalf("usage %d bytes while the base shares all data with the project", shared)
	}

	writeFile(t, filepath.Join(root, "big.bin"), bytes.Repeat([]byte("y"), 4<<20))
	private, err := s.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if private < 4<<20 {
		t.Fatalf("usage %d bytes after the project's copy was rewritten, want at least 4 MiB", private)
	}
}

func TestPruneBySizeMergesUntilUnder(t *testing.T) {
	s, root := newProject(t)
	for _, fill := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(root, "big.bin"), bytes.Repeat([]byte(fill), 2<<20))
		if _, err := s.Take(store.CheckpointOptions{Label: "write " + fill, Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "big.bin"), bytes.Repeat([]byte("d"), 2<<20))
	want := fingerprint(root)
	if _, err := s.Take(store.CheckpointOptions{Label: "write d", Dir: root}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Usage()
	if before < 6<<20 {
		t.Fatalf("usage %d before pruning, want at least 6 MiB of old versions", before)
	}

	merged, err := s.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 3 << 20})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Usage()
	if merged == 0 || after > 3<<20 {
		t.Fatalf("merged %d, usage %d after pruning, want at most 3 MiB", merged, after)
	}
	list, _ := s.List()
	if list[len(list)-1].ID != 4 {
		t.Fatalf("latest checkpoint is %d, want 4", list[len(list)-1].ID)
	}
	if _, err := restore.Apply(s, 4, (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(root); !maps.Equal(got, want) {
		t.Fatal("restore to the latest checkpoint differs after pruning by size")
	}
}
