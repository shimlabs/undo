package clone

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/shimlabs/undo/internal/testutil"
)

// A copy keeps what a clone on macOS keeps: the extended attributes of files
// and folders.
func TestCopyAtKeepsExtendedAttributes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	file := filepath.Join(src, "tree", "tagged.txt")
	testutil.WriteFile(t, file, "content")
	for _, path := range []string{file, filepath.Dir(file)} {
		if err := unix.Setxattr(path, "user.example.tag", []byte("red"), 0); errors.Is(err, unix.ENOTSUP) {
			t.Skip("this filesystem keeps no user extended attributes")
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if err := copyByPath(t, CopyAt, filepath.Join(src, "tree"), filepath.Join(dst, "tree")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"tree/tagged.txt", "tree"} {
		value := make([]byte, 16)
		size, err := unix.Getxattr(filepath.Join(dst, rel), "user.example.tag", value)
		if err != nil || string(value[:size]) != "red" {
			t.Errorf("%s: user.example.tag = %q, %v; want red", rel, value[:max(size, 0)], err)
		}
	}
}
