package clone

import (
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

// On exFAT, macOS keeps a file's extended attributes in a "._" file beside
// it, which it makes as soon as the file is copied. The copy of a "._" file
// from the same folder must then replace it.
func TestCopyAtReplacesAppleDoubleFileOnExFAT(t *testing.T) {
	src, dst := t.TempDir(), testutil.ExFATDir(t)
	testutil.WriteFile(t, filepath.Join(src, "._b.ts"), "project metadata")
	testutil.WriteFile(t, filepath.Join(dst, "._b.ts"), "made by the kernel")
	if err := copyByPath(t, CopyAt, filepath.Join(src, "._b.ts"), filepath.Join(dst, "._b.ts")); err != nil {
		t.Fatal(err)
	}
	testutil.AssertContent(t, filepath.Join(dst, "._b.ts"), "project metadata")
}

// Copy mode exists for filesystems such as exFAT, and a copy there must keep
// what a clone keeps: Finder tags and other extended attributes, of files and
// of folders.
func TestCopyAtKeepsExtendedAttributes(t *testing.T) {
	checkCopyKeepsExtendedAttributes(t, testutil.ExFATDir(t), "com.example.tag")
}
