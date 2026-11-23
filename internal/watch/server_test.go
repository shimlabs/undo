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

// newServerProject returns a project, its store and a socket directory.
func newServerProject(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	// Socket paths are limited to about 100 bytes, and the test's own
	// temporary directory can be longer than that.
	socketDir, err := os.MkdirTemp("/tmp", "undo-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	root, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(root, "main.go"), "package main")
	s, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root, socketDir
}

// runServer runs Serve until the test ends, then stops it and waits for it to
// exit before the test's directories are removed. It returns once the server
// accepts connections, with a channel that receives Serve's result and a
// function that stops the server early.
func runServer(t *testing.T, s *store.Store, options Options) (done chan error, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	options.Ready = func() { close(ready) }
	result := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		result <- Serve(ctx, s, options)
		close(exited)
	}()
	t.Cleanup(func() {
		cancel()
		<-exited
	})
	select {
	case <-ready:
	case err := <-result:
		t.Fatalf("server stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return result, cancel
}

func checkpointCount(t *testing.T, client Client, s *store.Store) int {
	t.Helper()
	status, err := client.Status(s)
	if err != nil {
		t.Fatal(err)
	}
	return status.CheckpointCount
}

func waitForCheckpointCount(t *testing.T, s *store.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		checkpoints, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(checkpoints) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("store has %d checkpoints, want %d", len(checkpoints), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func takeEdits(t *testing.T, client Client, s *store.Store, root string, count int) {
	t.Helper()
	for i := range count {
		writeFile(t, filepath.Join(root, "main.go"), fmt.Sprintf("package main // %d", i))
		if _, err := client.Checkpoint(s, store.CheckpointOptions{Label: "edit", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckpointUsesRunningServer(t *testing.T) {
	s, root, socketDir := newServerProject(t)
	runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: time.Minute})
	client := Client{SocketDir: socketDir}

	takeEdits(t, client, s, root, 2)

	if count := checkpointCount(t, client, s); count != 2 {
		t.Fatalf("the server took %d checkpoints, want 2", count)
	}
	latest, _, _ := s.Latest()
	manifest, _ := s.Manifest(latest.ID)
	if _, ok := manifest.Changed["main.go"]; !ok {
		t.Fatalf("layer %d = %+v, want main.go", latest.ID, manifest)
	}
}

func TestCheckpointFallsBackWithoutServer(t *testing.T) {
	s, root, socketDir := newServerProject(t)

	checkpoint, err := Client{SocketDir: socketDir}.Checkpoint(s, store.CheckpointOptions{Label: "no server", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ID != 1 {
		t.Fatalf("id = %d, want 1", checkpoint.ID)
	}
}

func TestCheckpointStartsServer(t *testing.T) {
	s, root, socketDir := newServerProject(t)
	isStarted := false
	client := Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) {
		isStarted = true
		runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: time.Minute})
		return nil, nil
	}}

	if _, err := client.Checkpoint(s, store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if !isStarted {
		t.Fatal("Start was not called")
	}
	if count := checkpointCount(t, client, s); count != 1 {
		t.Fatalf("the started server took %d checkpoints, want 1", count)
	}
}

func TestCheckpointRemembersFailedStart(t *testing.T) {
	s, root, socketDir := newServerProject(t)
	starts := 0
	client := Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) {
		starts++
		exited := make(chan error, 1)
		exited <- errors.New("inotify watch limit reached")
		return exited, nil
	}}

	began := time.Now()
	for range 3 {
		if _, err := client.Checkpoint(s, store.CheckpointOptions{Label: "edit", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Fatalf("waited %v for a watcher that had already exited", waited)
	}
	if starts != 1 {
		t.Fatalf("the watcher was started %d times, want once until the failure is old", starts)
	}
	if reason, isRecent := StartFailure(s); !isRecent || !strings.Contains(reason, "watch limit") {
		t.Fatalf("StartFailure = %q, %v", reason, isRecent)
	}
}

func TestCheckpointLogsWhyItScanned(t *testing.T) {
	s, root, _ := newServerProject(t)
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	client := Client{SocketDir: shared, Start: func(string) (<-chan error, error) { return nil, nil }}

	if _, err := client.Checkpoint(s, store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if last := s.LastError(); !strings.Contains(last, "private") {
		t.Fatalf("last error = %q, want the socket directory problem", last)
	}
}

func TestSocketDirRefusesSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "undo")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}

	if err := checkSocketDir(link); err == nil {
		t.Fatal("checkSocketDir accepted a symlink")
	}
}

func TestServeStopsWhenIdle(t *testing.T) {
	s, _, socketDir := newServerProject(t)
	done, _ := runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: 200 * time.Millisecond})

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
	s, _, socketDir := newServerProject(t)
	done, stop := runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: time.Minute})

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after its context was canceled")
	}
	if status, _ := (Client{SocketDir: socketDir}).Status(s); status.IsRunning {
		t.Fatal("server still answers after stopping")
	}
}

func TestSecondServerExits(t *testing.T) {
	s, _, socketDir := newServerProject(t)
	runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: time.Minute})

	if err := Serve(t.Context(), s, Options{SocketDir: socketDir, IdleTimeout: time.Minute}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Serve = %v, want ErrRunning", err)
	}
}

func TestServerPrunesWhenQuiet(t *testing.T) {
	s, root, socketDir := newServerProject(t)
	keepOne := store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	runServer(t, s, Options{SocketDir: socketDir, IdleTimeout: time.Minute, Limits: keepOne, PruneQuietPeriod: 500 * time.Millisecond})

	takeEdits(t, Client{SocketDir: socketDir}, s, root, 3)

	if checkpoints, _ := s.List(); len(checkpoints) != 3 {
		t.Fatalf("checkpoints = %d right after the last request, want 3 until the server is quiet", len(checkpoints))
	}
	waitForCheckpointCount(t, s, 1)
}
