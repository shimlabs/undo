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

// runServer runs Serve until the test ends and returns the channel that
// receives its result.
func runServer(t *testing.T, s *store.Store, options Options) chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- Serve(ctx, s, options)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return done
}

func startServer(t *testing.T, s *store.Store, idle time.Duration) chan error {
	t.Helper()
	ready := make(chan struct{})
	done := runServer(t, s, Options{Idle: idle, Limits: store.DefaultLimits, Ready: func() { close(ready) }})
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
		runServer(t, s, Options{Idle: time.Minute, Limits: store.DefaultLimits})
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

func TestServeStopsWhenCanceled(t *testing.T) {
	s, _ := serverProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, s, Options{Idle: time.Minute, Ready: func() { close(ready) }}) }()
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
	if status, _ := Status(s); status.Running {
		t.Fatal("server still answers after stopping")
	}
}

func TestSecondServerExits(t *testing.T) {
	s, _ := serverProject(t)
	startServer(t, s, time.Minute)

	if err := Serve(context.Background(), s, Options{Idle: time.Minute, Limits: store.DefaultLimits}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Serve = %v, want ErrRunning", err)
	}
}

func TestServerPrunesAfterCheckpoints(t *testing.T) {
	s, root := serverProject(t)
	pruneInterval, pruneQuiet = 0, 20*time.Millisecond
	t.Cleanup(func() { pruneInterval, pruneQuiet = time.Hour, 30*time.Second })
	ready := make(chan struct{})
	limits := store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	runServer(t, s, Options{Idle: time.Minute, Limits: limits, Ready: func() { close(ready) }})
	<-ready

	for i := range 3 {
		write(t, filepath.Join(root, "main.go"), fmt.Sprintf("package main // %d", i))
		if _, err := Checkpoint(s, "edit", root, nil); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 && list[0].ID == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkpoints = %d after pruning, want only 3", len(list))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerPrunesOnlyWhenQuiet(t *testing.T) {
	s, root := serverProject(t)
	pruneInterval, pruneQuiet = 0, 500*time.Millisecond
	t.Cleanup(func() { pruneInterval, pruneQuiet = time.Hour, 30*time.Second })
	ready := make(chan struct{})
	limits := store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	runServer(t, s, Options{Idle: time.Minute, Limits: limits, Ready: func() { close(ready) }})
	<-ready

	for i := range 3 {
		write(t, filepath.Join(root, "main.go"), fmt.Sprintf("package main // %d", i))
		if _, err := Checkpoint(s, "edit", root, nil); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := s.List(); len(list) != 3 {
		t.Fatalf("checkpoints = %d right after the last request, want 3 until the server is quiet", len(list))
	}
	isPruned := func() bool {
		list, _ := s.List()
		return len(list) == 1
	}
	deadline := time.Now().Add(5 * time.Second)
	for !isPruned() {
		if time.Now().After(deadline) {
			t.Fatal("the server did not prune after going quiet")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lastServed(s *store.Store) (int, error) {
	status, err := Status(s)
	return status.LastID, err
}

func TestCheckpointLogsWhyItScanned(t *testing.T) {
	s, root := serverProject(t)
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNDO_SOCKET_DIR", shared)

	if _, err := Checkpoint(s, "first", root, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if last := s.LastError(); !strings.Contains(last, "private") {
		t.Fatalf("last error = %q, want the socket directory problem", last)
	}
}
