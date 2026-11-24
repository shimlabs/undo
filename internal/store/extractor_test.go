package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestSameBytes(t *testing.T) {
	long := strings.Repeat("x", 3*contentChunkSize+7)
	tests := []struct {
		first, second string
		want          bool
	}{
		{"", "", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "abcd", false},
		{"abcd", "abc", false},
		{long, long, true},
		{long, long[:len(long)-1] + "y", false},
		{long, long + "z", false},
	}
	for _, test := range tests {
		got, err := sameBytes(strings.NewReader(test.first), strings.NewReader(test.second), make([]byte, contentChunkSize), make([]byte, contentChunkSize))
		if err != nil || got != test.want {
			t.Errorf("sameBytes(%d bytes, %d bytes) = %v, %v; want %v", len(test.first), len(test.second), got, err, test.want)
		}
	}
}

// The store opens its files through a root, whose errors name paths inside
// the store. Callers report a relative path as one in the project, so an
// error from the store names its full path.
func TestExtractNamesStoredFolderInFull(t *testing.T) {
	testutil.SkipIfRoot(t)
	projectStore, root := newProject(t, map[string]string{"src/a.txt": "a"})
	take(t, projectStore, "first")
	storedDir := filepath.Dir(projectStore.basePath("src/a.txt"))
	if err := os.Chmod(storedDir, 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, storedDir, 0o700)

	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	extractor := projectStore.NewExtractor()
	defer extractor.Close()
	err = extractor.ExtractAt("src/a.txt", Source{}, dir, "a.txt")
	if pathErr, ok := errors.AsType[*fs.PathError](err); !ok || pathErr.Path != storedDir {
		t.Fatalf("ExtractAt = %v, want an error naming %s", err, storedDir)
	}
}
