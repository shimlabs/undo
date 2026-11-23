package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestNewTempPathIsUniqueAndHidden(t *testing.T) {
	first, second := newTempPath("/p/main.go"), newTempPath("/p/main.go")
	if first == second {
		t.Fatal("newTempPath returned the same name twice")
	}
	if filepath.Dir(first) != "/p" || !strings.HasPrefix(filepath.Base(first), ".main.go.undo-") {
		t.Fatalf("newTempPath = %q", first)
	}
}
