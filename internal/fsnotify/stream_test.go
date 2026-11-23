//go:build darwin || linux

package fsnotify

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
)

type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) add(events []Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, events...)
}

func (c *collector) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make([]string, 0, len(c.events))
	for _, event := range c.events {
		paths = append(paths, event.Path)
	}
	return paths
}

// waitFor creates marker and returns the paths delivered before its event.
func (c *collector) waitFor(t *testing.T, marker string) []string {
	t.Helper()
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		paths := c.paths()
		if i := slices.Index(paths, marker); i >= 0 {
			return paths[:i]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("marker event never arrived")
	return nil
}

func startCollecting(t *testing.T, root string) *collector {
	t.Helper()
	var collected collector
	stream, err := NewStream([]string{root}, nil, collected.add)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stream.Close)
	return &collected
}

func TestStreamDeliversWritesBeforeLaterMarker(t *testing.T) {
	root := testutil.TempDir(t)
	collected := startCollecting(t, root)

	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if before := collected.waitFor(t, filepath.Join(root, "marker")); !slices.Contains(before, path) {
		t.Fatalf("events before marker = %v, want %s", before, path)
	}
}

func TestStreamDeliversDeletesBeforeLaterMarker(t *testing.T) {
	root := testutil.TempDir(t)
	nested := filepath.Join(root, "src", "lib", "x.go")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, nested, "x")
	collected := startCollecting(t, root)
	collected.waitFor(t, filepath.Join(root, "ready"))

	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}

	before := collected.waitFor(t, filepath.Join(root, "marker"))
	if !slices.Contains(before, nested) || !slices.Contains(before, filepath.Join(root, "src")) {
		t.Fatalf("events before marker = %v", before)
	}
}

func TestStreamCloseWhileEventsArrive(t *testing.T) {
	root := testutil.TempDir(t)
	for round := range 50 {
		stream, err := NewStream([]string{root}, nil, func([]Event) {})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 20 {
			if err := os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i))), []byte{byte(round)}, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		stream.Close()
	}
	// A callback that still fires after Close would crash in this window,
	// inside this test rather than a later one.
	time.Sleep(200 * time.Millisecond)
}

func TestStreamSeesFilesInNewDirectories(t *testing.T) {
	root := testutil.TempDir(t)
	collected := startCollecting(t, root)

	nested := filepath.Join(root, "a", "b", "c", "f.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, nested, "x")
	collected.waitFor(t, filepath.Join(root, "ready"))
	testutil.WriteFile(t, nested, "changed")

	if before := collected.waitFor(t, filepath.Join(root, "marker")); !slices.Contains(before, nested) {
		t.Fatalf("events before marker = %v, want %s", before, nested)
	}
}

func TestStreamWatchesRecreatedRoot(t *testing.T) {
	root := testutil.TempDir(t)
	markers := testutil.TempDir(t)
	var collected collector
	stream, err := NewStream([]string{root, markers}, nil, collected.add)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stream.Close)

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	collected.waitFor(t, filepath.Join(markers, "recreated"))

	path := filepath.Join(root, "a.txt")
	testutil.WriteFile(t, path, "x")
	if before := collected.waitFor(t, filepath.Join(markers, "written")); !slices.Contains(before, path) {
		t.Fatalf("events before marker = %v, want %s", before, path)
	}
}
