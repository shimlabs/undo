package restore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/watch"
)

// startWatcher runs a watcher for projectStore until the test ends, and
// returns a client that only uses it. Pruning is left to the test.
func startWatcher(t *testing.T, projectStore *store.Store) watch.Client {
	t.Helper()
	socketDir := testutil.SocketDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	exited := make(chan error, 1)
	go func() {
		exited <- watch.Serve(ctx, projectStore, watch.Options{
			SocketDir:        socketDir,
			IdleTimeout:      time.Hour,
			Limits:           store.Limits{MaxAge: 24 * time.Hour, MaxCount: 1 << 20, MaxBytes: 1 << 40},
			PruneQuietPeriod: time.Hour,
			Ready:            func() { close(ready) },
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-exited; err != nil {
			t.Errorf("watcher: %v", err)
		}
	})
	select {
	case <-ready:
	case err := <-exited:
		t.Fatalf("watcher stopped early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not start")
	}
	return watch.Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) { return nil, errWatcherGone }}
}

// Folders made in quick succession, as mkdir -p, git clone or tar make them.
// Every later edit inside them must reach the checkpoints.
func TestWatcherSavesEditsInFoldersMadeTogether(t *testing.T) {
	root := testutil.TempDir(t)
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projectStore.Close() })
	checkpointer := watcherCheckpointer(t, startWatcher(t, projectStore))
	takeThroughWatcher := func(label string) store.Checkpoint {
		t.Helper()
		checkpoint, err := checkpointer(projectStore, store.CheckpointOptions{Label: label, Dir: root})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	testutil.WriteFile(t, filepath.Join(root, "README"), "r")
	takeThroughWatcher("1")
	files := make([]string, 0, 80)
	for i := range 20 {
		for j := range 4 {
			files = append(files, filepath.Join(root, fmt.Sprintf("pkg%d", i), fmt.Sprintf("sub%d", j), "f.txt"))
		}
	}
	for _, file := range files {
		testutil.WriteFile(t, file, "v1")
	}
	takeThroughWatcher("2")
	for _, file := range files {
		testutil.WriteFile(t, file, "v2 longer")
	}
	third := takeThroughWatcher("3")
	if differences := listingDifferences(t, projectStore, third.ID, true); differences != "" {
		t.Fatalf("checkpoint 3 differs from a scan:\n%s", differences)
	}
	for _, file := range files {
		testutil.WriteFile(t, file, "v3 even longer")
	}
	takeThroughWatcher("4")
	apply(t, projectStore, third.ID)
	for _, file := range files {
		testutil.AssertContent(t, file, "v2 longer")
	}
}

// watcherCheckpointer takes checkpoints through client, and fails the test
// when one was taken by a scan instead.
func watcherCheckpointer(t *testing.T, client watch.Client) store.Checkpointer {
	t.Helper()
	return func(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		checkpoint, err := client.Checkpoint(projectStore, options)
		if line := projectStore.LastError(); line != "" {
			t.Fatalf("the checkpoint did not go through the watcher: %s", line)
		}
		return checkpoint, err
	}
}
