package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

func newServerProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "undo-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	t.Setenv("UNDO_SOCKET_DIR", socketDir)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(root, "main.go"), "package main")
	s, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// runServer runs Serve until the test ends and returns the channel that
// receives its result. It returns once the server accepts connections, or
// fails the test if it stops first.
func runServer(t *testing.T, s *store.Store, options Options) chan error {
	t.Helper()
	ready := make(chan struct{})
	options.Ready = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- Serve(t.Context(), s, options) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("server stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return done
}

func checkpointCount(t *testing.T, s *store.Store) int {
	t.Helper()
	status, err := Status(s)
	if err != nil {
		t.Fatal(err)
	}
	return status.CheckpointCount
}

func waitForCheckpointCount(t *testing.T, s *store.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("store has %d checkpoints, want %d", len(list), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func takeEdits(t *testing.T, s *store.Store, root string, count int) {
	t.Helper()
	for i := range count {
		writeFile(t, filepath.Join(root, "main.go"), fmt.Sprintf("package main // %d", i))
		if _, err := Checkpoint(s, store.CheckpointOptions{Label: "edit", Dir: root}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckpointUsesRunningServer(t *testing.T) {
	s, root := newServerProject(t)
	runServer(t, s, Options{IdleTimeout: time.Minute})

	takeEdits(t, s, root, 2)

	if count := checkpointCount(t, s); count != 2 {
		t.Fatalf("the server took %d checkpoints, want 2", count)
	}
	latest, _, _ := s.Latest()
	manifest, _ := s.Manifest(latest.ID)
	if _, ok := manifest.Changed["main.go"]; !ok {
		t.Fatalf("layer %d = %+v, want main.go", latest.ID, manifest)
	}
}

func TestCheckpointFallsBackWithoutServer(t *testing.T) {
	s, root := newServerProject(t)

	cp, err := Checkpoint(s, store.CheckpointOptions{Label: "no server", Dir: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID != 1 {
		t.Fatalf("id = %d, want 1", cp.ID)
	}
}

func TestCheckpointStartsServer(t *testing.T) {
	s, root := newServerProject(t)
	isStarted := false
	start := func() (<-chan error, error) {
		isStarted = true
		go Serve(t.Context(), s, Options{IdleTimeout: time.Minute})
		return nil, nil
	}

	if _, err := Checkpoint(s, store.CheckpointOptions{Label: "first", Dir: root}, start); err != nil {
		t.Fatal(err)
	}
	if !isStarted {
		t.Fatal("start was not called")
	}
	if count := checkpointCount(t, s); count != 1 {
		t.Fatalf("the started server took %d checkpoints, want 1", count)
	}
}

func TestCheckpointStopsWaitingWhenServerExits(t *testing.T) {
	s, root := newServerProject(t)
	start := func() (<-chan error, error) {
		exited := make(chan error, 1)
		exited <- errors.New("bind failed")
		return exited, nil
	}

	began := time.Now()
	if _, err := Checkpoint(s, store.CheckpointOptions{Label: "first", Dir: root}, start); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited > time.Second {
		t.Fatalf("waited %v for a watcher that had already exited", waited)
	}
	if !strings.Contains(s.LastError(), "bind failed") {
		t.Fatalf("last error = %q, want why the watcher exited", s.LastError())
	}
}

func TestCheckpointLogsWhyItScanned(t *testing.T) {
	s, root := newServerProject(t)
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNDO_SOCKET_DIR", shared)

	if _, err := Checkpoint(s, store.CheckpointOptions{Label: "first", Dir: root}, func() (<-chan error, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if last := s.LastError(); !strings.Contains(last, "private") {
		t.Fatalf("last error = %q, want the socket directory problem", last)
	}
}

func TestSocketDirRefusesSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "undo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNDO_SOCKET_DIR", link)

	if _, err := socketDir(); err == nil {
		t.Fatal("socketDir accepted a symlink")
	}
}

func TestServeStopsWhenIdle(t *testing.T) {
	s, _ := newServerProject(t)
	done := runServer(t, s, Options{IdleTimeout: 200 * time.Millisecond})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running while idle")
	}
}

func TestServeStopsWhenCanceled(t *testing.T) {
	s, _ := newServerProject(t)
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, s, Options{IdleTimeout: time.Minute, Ready: func() { close(ready) }}) }()
	<-ready

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after its context was canceled")
	}
	if status, _ := Status(s); status.IsRunning {
		t.Fatal("server still answers after stopping")
	}
}

func TestSecondServerExits(t *testing.T) {
	s, _ := newServerProject(t)
	runServer(t, s, Options{IdleTimeout: time.Minute})

	if err := Serve(t.Context(), s, Options{IdleTimeout: time.Minute}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Serve = %v, want ErrRunning", err)
	}
}

func TestServerPrunesWhenQuiet(t *testing.T) {
	s, root := newServerProject(t)
	keepOne := store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	runServer(t, s, Options{IdleTimeout: time.Minute, Limits: keepOne, PruneQuietPeriod: 500 * time.Millisecond})

	takeEdits(t, s, root, 3)

	if list, _ := s.List(); len(list) != 3 {
		t.Fatalf("checkpoints = %d right after the last request, want 3 until the server is quiet", len(list))
	}
	waitForCheckpointCount(t, s, 1)
}
