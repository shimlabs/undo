//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The shell can still be in a folder an agent deleted, which getcwd(3)
// cannot name on Linux.
func TestWorkingDirInsideDeletedFolder(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "gone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("PWD", dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	got, err := workingDir()
	if err != nil || got != dir {
		t.Fatalf("workingDir() = %q, %v; want %q", got, err, dir)
	}
}
