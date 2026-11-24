package restore

import (
	"testing"

	"github.com/shimlabs/undo/internal/tree"
)

// XFS gives a new file the inode number of one just removed, so a file a
// restore wrote back can have the checkpoint's inode number and still be
// another file. Clones carry no extended attributes on Linux, so its content
// is all there is to compare.
func TestDiffersOnlyInIdentityComparesFileWithReusedInode(t *testing.T) {
	target := tree.Entry{Kind: tree.File, Mode: 0o644, Size: 3, ModTime: 10, ChangeTime: 10, Inode: 7}
	current := target
	current.ChangeTime = 20
	if !differsOnlyInIdentity(current, target) {
		t.Error("a file with the checkpoint's inode number and a new change time is not compared")
	}
}
