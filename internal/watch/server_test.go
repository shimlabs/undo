package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

func serverProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	socketDir, err := os.MkdirTemp("/tmp", "undo-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	t.Setenv("UNDO_SOCKET_DIR", socketDir)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(root, "main.go"), "package main")
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func startServer(t *testing.T, s *store.Store, idle time.Duration) chan error {
	t.Helper()
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- Serve(s, idle, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("server stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return done
}

func TestCheckpointUsesRunningServer(t *testing.T) {
	s, root := serverProject(t)
	startServer(t, s, time.Minute)
	Checkpoint(s, "first", root, nil)

	write(t, filepath.Join(root, "main.go"), "package main // edited")
	cp, err := Checkpoint(s, "second", root, nil)
	if err != nil {
		t.Fatal(err)
	}

	m, err := s.Layer(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Changed["main.go"]; !ok {
		t.Fatalf("layer %d = %+v, want main.go", cp.ID, m)
	}
	if used, _ := lastServed(s); used != cp.ID {
		t.Fatalf("checkpoint %d was not taken by the server (last served %d)", cp.ID, used)
	}
}

func TestCheckpointFallsBackWithoutServer(t *testing.T) {
	s, root := serverProject(t)

	cp, err := Checkpoint(s, "no server", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID != 1 {
		t.Fatalf("id = %d, want 1", cp.ID)
	}
}

func TestCheckpointStartsServer(t *testing.T) {
	s, root := serverProject(t)
	started := false
	start := func() error {
		started = true
		go Serve(s, time.Minute, nil)
		return nil
	}

	cp, err := Checkpoint(s, "first", root, start)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("start was not called")
	}
	if used, _ := lastServed(s); used != cp.ID {
		t.Fatalf("checkpoint %d was not taken by the started server", cp.ID)
	}
}

func TestServeStopsWhenIdle(t *testing.T) {
	s, _ := serverProject(t)
	done := startServer(t, s, 200*time.Millisecond)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running while idle")
	}
}

func TestSecondServerExits(t *testing.T) {
	s, _ := serverProject(t)
	startServer(t, s, time.Minute)

	if err := Serve(s, time.Minute, nil); err != ErrRunning {
		t.Fatalf("second Serve = %v, want ErrRunning", err)
	}
}

func lastServed(s *store.Store) (int, error) {
	status, err := Status(s)
	return status.LastID, err
}
