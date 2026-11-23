package watch

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/testutil"
)

func checkSwap(t *testing.T, a, b string, makeA, makeB func(*testing.T, string)) {
	t.Helper()
	projectTracker, projectStore, root := newTrackerWithFiles(t)
	makeA(t, filepath.Join(root, a))
	makeB(t, filepath.Join(root, b))
	takeCheckpoint(t, projectTracker, "1")
	takeCheckpoint(t, projectTracker, "2")
	if err := unix.RenamexNp(filepath.Join(root, a), filepath.Join(root, b), unix.RENAME_SWAP); err != nil {
		t.Fatal(err)
	}
	third := takeCheckpoint(t, projectTracker, "3")
	checkMatchesScan(t, projectStore, root, third.ID)
}

func dirWithFile(name string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		testutil.WriteFile(t, filepath.Join(dir, name, "f.txt"), name)
	}
}

func fileWith(content string) func(*testing.T, string) {
	return func(t *testing.T, file string) {
		t.Helper()
		testutil.WriteFile(t, file, content)
	}
}

func TestSwapFiles(t *testing.T) { checkSwap(t, "x.txt", "y.txt", fileWith("x"), fileWith("yy")) }
func TestSwapDirs(t *testing.T)  { checkSwap(t, "dx", "dy", dirWithFile("one"), dirWithFile("two")) }
func TestSwapDirWithExcluded(t *testing.T) {
	checkSwap(t, "lib", "node_modules", dirWithFile("one"), dirWithFile("pkg"))
}
func TestSwapFileWithDir(t *testing.T) {
	checkSwap(t, "x.txt", "dy", fileWith("x"), dirWithFile("two"))
}
