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
	assertContent(t, filepath.Join(root, longName), "v1")
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
	assertContent(t, filepath.Join(root, longName), "v2")
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
	assertContent(t, filepath.Join(root, latin1Name), "v1")
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
	assertContent(t, filepath.Join(root, latin1Name), "v2")
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s holds %q, %v; want %q", filepath.Base(path), data, err, want)
	}
}
