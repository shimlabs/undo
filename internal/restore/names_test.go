package restore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

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
