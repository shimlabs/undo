package restore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
)

// XFS gives a new file the inode number of one just removed, so a file a
// restore wrote back can have the checkpoint's inode number and still be
// another file. Clones carry no extended attributes on Linux, so its content
// is all there is to compare.
func TestDiffersOnlyInIdentityComparesFileWithReusedInode(t *testing.T) {
	target := tree.Entry{Kind: tree.File, Mode: 0o644, Size: 3, ModTime: 10, ChangeTime: 10, Inode: 7}
	current := target
	current.ChangeTime = 20
	if !differsOnlyInIdentity(current, target, false) {
		t.Error("a file with the checkpoint's inode number and a new change time is not compared")
	}
}

// Copy mode keeps extended attributes, so a file whose only change is one of
// them must be written back, even though its inode stays the same.
func TestRestoreBringsBackExtendedAttributesInCopyMode(t *testing.T) {
	root := testutil.TempDir(t)
	file := filepath.Join(root, "data.txt")
	testutil.WriteFile(t, file, "content")
	if err := unix.Setxattr(file, "user.tag", []byte("one"), 0); errors.Is(err, unix.ENOTSUP) {
		t.Skip("this filesystem keeps no user extended attributes")
	} else if err != nil {
		t.Fatal(err)
	}
	projectStore, err := store.Create(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectStore.SetCopyMode(true); err != nil {
		t.Fatal(err)
	}
	first := take(t, projectStore, "1")
	if err := unix.Setxattr(file, "user.tag", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}

	apply(t, projectStore, first.ID)
	value := make([]byte, 16)
	size, err := unix.Getxattr(file, "user.tag", value)
	if err != nil || string(value[:size]) != "one" {
		t.Fatalf("user.tag = %q, %v; want one", value[:max(size, 0)], err)
	}
}
