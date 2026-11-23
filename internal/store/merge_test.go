package store

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func stateHashes(t *testing.T, s *Store, id int) map[string][32]byte {
	t.Helper()
	state, err := s.State(id)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string][32]byte{}
	for rel, source := range state {
		path := s.basePath(rel)
		if source.Layer > 0 {
			path = s.layerFile(source.Layer, rel)
		}
		data, _ := os.ReadFile(path)
		hashes[rel] = sha256.Sum256(append([]byte(source.Entry.Mode.String()), data...))
	}
	return hashes
}

func TestPruneFinishesInterruptedMerge(t *testing.T) {
	s, root := newProject(t, nil)
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
		if _, err := s.Take(CheckpointOptions{Label: "step", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	wantAtThird, wantAtFourth := stateHashes(t, s, 3), stateHashes(t, s, 4)

	base, err := s.BaseListing()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(s.mergePath(), pendingMerge{Through: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mergeLayer(2, base); err != nil {
		t.Fatal(err)
	}

	unlimited := Limits{MaxAge: time.Hour, MaxCount: 100, MaxBytes: 1 << 40}
	if _, err := s.Prune(time.Now(), unlimited); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 2 || list[0].ID != 3 {
		t.Fatalf("checkpoints = %+v, want 3 and 4", list)
	}
	for id, want := range map[int]map[string][32]byte{3: wantAtThird, 4: wantAtFourth} {
		got := stateHashes(t, s, id)
		if len(got) != len(want) {
			t.Fatalf("state %d has %d paths, want %d", id, len(got), len(want))
		}
		for rel, hash := range want {
			if got[rel] != hash {
				t.Errorf("state %d: %s differs", id, rel)
			}
		}
	}
	if _, err := os.Stat(s.mergePath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("merge record left behind: %v", err)
	}
}

func TestPruneWaitsForReaders(t *testing.T) {
	s, _ := newProject(t, nil)
	unlock, err := s.RLock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Prune(time.Now(), DefaultLimits)
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
