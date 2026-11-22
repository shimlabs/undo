package clone

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloneFileRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := File(src, dst); err == nil {
		t.Fatal("File overwrote an existing destination")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "old" {
		t.Fatalf("destination changed to %q", data)
	}
}

func TestCloneTreeCopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := Tree(src, dst); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "a", "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("cloned content = %q", data)
	}
	info, _ := os.Stat(filepath.Join(dst, "a", "f.txt"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cloned mode = %v, want 0600", info.Mode().Perm())
	}
}
