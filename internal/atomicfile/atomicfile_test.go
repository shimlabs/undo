package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestTempNameIsUniqueAndHidden(t *testing.T) {
	first, second := TempName("main.go"), TempName("main.go")
	if first == second {
		t.Fatal("TempName returned the same name twice")
	}
	if !strings.HasPrefix(first, ".main.go.undo-") {
		t.Fatalf("TempName = %q", first)
	}
}

func TestTempNameFitsLongestName(t *testing.T) {
	name := strings.Repeat("é", 127) + "a"
	got := TempName(name)
	if len(got) > 255 {
		t.Fatalf("TempName of a %d-byte name is %d bytes, over NAME_MAX", len(name), len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("TempName cut a character in two: %q", got)
	}
}
