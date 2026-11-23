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
	"github.com/shimlabs/undo/internal/testutil"
)

// newServerProject returns a project's store, its root and a socket
// directory.
func newServerProject(t *testing.T) (projectStore *store.Store, root, socketDir string) {
	t.Helper()
	// Socket paths are limited to about 100 bytes, and the test's own
	// temporary directory can be longer than that.
	socketDir, err := os.MkdirTemp("/tmp", "undo-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	root = testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	if projectStore, err = store.Create(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	return projectStore, root, socketDir
}

// runServer runs Serve until the test ends, then stops it and waits for it to
// exit before the test's directories are removed. It returns once the server
// accepts connections, with a channel that receives Serve's result and a
// function that stops the server early.
func runServer(t *testing.T, projectStore *store.Store, options Options) (done chan error, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	options.Ready = func() { close(ready) }
	result := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		result <- Serve(ctx, projectStore, options)
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

func checkpointCount(t *testing.T, client Client, projectStore *store.Store) int {
	t.Helper()
	status, err := client.Status(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	return status.CheckpointCount
}

func waitForCheckpointCount(t *testing.T, projectStore *store.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		checkpoints, err := projectStore.List()
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

func takeEdits(t *testing.T, client Client, projectStore *store.Store, root string, count int) {
	t.Helper()
	for i := range count {
		testutil.WriteFile(t, filepath.Join(root, "main.go"), fmt.Sprintf("package main // %d", i))
		if _, err := client.Checkpoint(projectStore, store.CheckpointOptions{Label: "edit", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckpointUsesRunningServer(t *testing.T) {
	projectStore, root, socketDir := newServerProject(t)
	runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute})
	client := Client{SocketDir: socketDir}

	takeEdits(t, client, projectStore, root, 2)

	if count := checkpointCount(t, client, projectStore); count != 2 {
		t.Fatalf("the server took %d checkpoints, want 2", count)
	}
	latest, _, _ := projectStore.Latest()
	manifest, _ := projectStore.Manifest(latest.ID)
	if _, ok := manifest.Changed["main.go"]; !ok {
		t.Fatalf("layer %d = %+v, want main.go", latest.ID, manifest)
	}
}

func TestCheckpointFallsBackWithoutServer(t *testing.T) {
	projectStore, root, socketDir := newServerProject(t)

	checkpoint, err := Client{SocketDir: socketDir}.Checkpoint(projectStore, store.CheckpointOptions{Label: "no server", Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ID != 1 {
		t.Fatalf("id = %d, want 1", checkpoint.ID)
	}
}

func TestCheckpointStartsServer(t *testing.T) {
	projectStore, root, socketDir := newServerProject(t)
	isStarted := false
	client := Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) {
		isStarted = true
		runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute})
		return nil, nil
	}}

	if _, err := client.Checkpoint(projectStore, store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if !isStarted {
		t.Fatal("Start was not called")
	}
	if count := checkpointCount(t, client, projectStore); count != 1 {
		t.Fatalf("the started server took %d checkpoints, want 1", count)
	}
}

func TestCheckpointRemembersFailedStart(t *testing.T) {
	projectStore, root, socketDir := newServerProject(t)
	starts := 0
	client := Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) {
		starts++
		exited := make(chan error, 1)
		exited <- errors.New("inotify watch limit reached")
		return exited, nil
	}}

	// The first checkpoint clones the project, which takes long on a slow
	// disk. Only the time spent on the failed watcher matters.
	if _, err := projectStore.Take(store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for range 3 {
		if _, err := client.Checkpoint(projectStore, store.CheckpointOptions{Label: "edit", Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	if waited := time.Since(began); waited >= startTimeout {
		t.Fatalf("waited %v for a watcher that had already exited", waited)
	}
	if starts != 1 {
		t.Fatalf("the watcher was started %d times, want once until the failure is old", starts)
	}
	if reason, isRecent := StartFailure(projectStore); !isRecent || !strings.Contains(reason, "watch limit") {
		t.Fatalf("StartFailure = %q, %v", reason, isRecent)
	}
}

func TestCheckpointLogsWhyItScanned(t *testing.T) {
	projectStore, root, _ := newServerProject(t)
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	client := Client{SocketDir: shared, Start: func(string) (<-chan error, error) { return nil, nil }}

	if _, err := client.Checkpoint(projectStore, store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if last := projectStore.LastError(); !strings.Contains(last, "private") {
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
	projectStore, _, socketDir := newServerProject(t)
	done, _ := runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: 200 * time.Millisecond})

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
	projectStore, _, socketDir := newServerProject(t)
	done, stop := runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute})

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after its context was canceled")
	}
	if status, _ := (Client{SocketDir: socketDir}).Status(projectStore); status.IsRunning {
		t.Fatal("server still answers after stopping")
	}
}

func TestSecondServerExits(t *testing.T) {
	projectStore, _, socketDir := newServerProject(t)
	runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute})

	if err := Serve(t.Context(), projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Serve = %v, want ErrRunning", err)
	}
}

func TestServerPrunesWhenQuiet(t *testing.T) {
	projectStore, root, socketDir := newServerProject(t)
	keepOne := store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}
	runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute, Limits: keepOne, PruneQuietPeriod: 500 * time.Millisecond})

	takeEdits(t, Client{SocketDir: socketDir}, projectStore, root, 3)

	if checkpoints, _ := projectStore.List(); len(checkpoints) != 3 {
		t.Fatalf("checkpoints = %d right after the last request, want 3 until the server is quiet", len(checkpoints))
	}
	waitForCheckpointCount(t, projectStore, 1)
}

func TestServerStopsWhenStreamCannotRestart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	projectStore, root, socketDir := newServerProject(t)
	done, _ := runServer(t, projectStore, Options{SocketDir: socketDir, IdleTimeout: time.Minute})
	client := Client{SocketDir: socketDir}
	if _, err := client.Checkpoint(projectStore, store.CheckpointOptions{Label: "first", Dir: root}); err != nil {
		t.Fatal(err)
	}
	ignoreFile := filepath.Join(root, ".undoignore")
	testutil.WriteFile(t, ignoreFile, "build/\n")
	if err := os.Chmod(ignoreFile, 0o000); err != nil {
		t.Fatal(err)
	}

	client.Checkpoint(projectStore, store.CheckpointOptions{Label: "rules changed", Dir: root})
	select {
	case err := <-done:
		if !errors.Is(err, errStreamStopped) {
			t.Fatalf("Serve returned %v, want errStreamStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running without a stream")
	}
}
