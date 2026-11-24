package clone

import (
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
	"golang.org/x/sys/unix"
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
	src, dst := t.TempDir(), testutil.ExFATDir(t)
	file := filepath.Join(src, "tree", "tagged.txt")
	testutil.WriteFile(t, file, "content")
	for _, path := range []string{file, filepath.Dir(file)} {
		if err := unix.Setxattr(path, "com.example.tag", []byte("red"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyByPath(t, CopyAt, filepath.Join(src, "tree"), filepath.Join(dst, "tree")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"tree/tagged.txt", "tree"} {
		value := make([]byte, 16)
		size, err := unix.Getxattr(filepath.Join(dst, rel), "com.example.tag", value)
		if err != nil || string(value[:size]) != "red" {
			t.Errorf("%s: com.example.tag = %q, %v; want red", rel, value[:max(size, 0)], err)
		}
	}
}
