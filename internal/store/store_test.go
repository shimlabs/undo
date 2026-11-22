package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/project"
)

func TestOpenCreatesStoreUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UNDO_HOME", home)
	root := t.TempDir()

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, project.ID(root))
	if s.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", s.Dir(), want)
	}
	info, err := os.Stat(filepath.Join(want, "layers"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("layers is not a directory")
	}
}
