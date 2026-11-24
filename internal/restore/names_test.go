package restore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
)

// longName is 244 bytes, near NAME_MAX, so a temporary name built from it
// overflows unless it is cut.
var longName = strings.Repeat("abcdefghij", 24) + ".txt" //nolint:gochecknoglobals // shared test fixture

func TestRestoreFileWithLongName(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, longName), "v1")
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, longName), "v2")
	take(t, projectStore, "2")
	apply(t, projectStore, 1)
	testutil.AssertContent(t, filepath.Join(root, longName), "v1")
}

func TestRestoreAfterMergingFileWithLongName(t *testing.T) {
	projectStore, root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, longName), "v1")
	take(t, projectStore, "1")
	testutil.WriteFile(t, filepath.Join(root, longName), "v2")
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "3")
	take(t, projectStore, "3")
	if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, longName), "v4")
	apply(t, projectStore, checkpoints[0].ID)
	testutil.AssertContent(t, filepath.Join(root, longName), "v2")
}

// latin1Name is "café.txt" in Latin-1: Linux filesystems accept it, but it
// is not valid UTF-8.
const latin1Name = "caf\xe9.txt"

func writeLatin1File(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, latin1Name), []byte(content), 0o600); err != nil {
		t.Skipf("the filesystem refuses names that are not UTF-8: %v", err)
	}
}

func TestRestoreKeepsNameThatIsNotUTF8(t *testing.T) {
	projectStore, root := newProject(t)
	take(t, projectStore, "1")
	writeLatin1File(t, root, "v1")
	take(t, projectStore, "2")
	testutil.WriteFile(t, filepath.Join(root, "README.md"), "changed")
	take(t, projectStore, "3")
	apply(t, projectStore, 2)
	testutil.AssertContent(t, filepath.Join(root, latin1Name), "v1")
}

func TestRestoreRemovedFileWithNameThatIsNotUTF8(t *testing.T) {
	projectStore, root := newProject(t)
	writeLatin1File(t, root, "v1")
	take(t, projectStore, "1")
	writeLatin1File(t, root, "v2")
	take(t, projectStore, "2")
	if err := os.Remove(filepath.Join(root, latin1Name)); err != nil {
		t.Fatal(err)
	}
	take(t, projectStore, "3")
	apply(t, projectStore, 2)
	testutil.AssertContent(t, filepath.Join(root, latin1Name), "v2")
}

func TestRestoreAndMergeNamesNearTheLimit(t *testing.T) {
	for _, name := range []string{
		strings.Repeat("x", 255),
		strings.Repeat("\u6f22", 83) + ".t", // 83 CJK characters, 3 bytes each
		strings.Repeat("a\xe9", 120) + "\xe9\xe9.txt",
	} {
		projectStore, root := newProject(t)
		if err := os.WriteFile(filepath.Join(root, name), []byte("v1"), 0o600); err != nil {
			t.Logf("the filesystem refuses %q: %v", name, err)
			continue
		}
		take(t, projectStore, "1")
		testutil.WriteFile(t, filepath.Join(root, name), "v2")
		take(t, projectStore, "2")
		testutil.WriteFile(t, filepath.Join(root, name), "v3")
		take(t, projectStore, "3")
		apply(t, projectStore, 2)
		testutil.AssertContent(t, filepath.Join(root, name), "v2")
		if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 1, MaxBytes: 1 << 40}); err != nil {
			t.Fatalf("prune: %v", err)
		}
		checkpoints, err := projectStore.List()
		if err != nil {
			t.Fatal(err)
		}
		testutil.WriteFile(t, filepath.Join(root, name), "v5")
		apply(t, projectStore, checkpoints[0].ID)
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".undo-") {
				t.Errorf("temporary file left: %q", entry.Name())
			}
		}
	}
}
