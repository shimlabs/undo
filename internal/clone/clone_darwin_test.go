package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	checkCopyKeepsExtendedAttributes(t, testutil.ExFATDir(t), "com.example.tag")
}

// replaceByRenameUntilDone keeps replacing the file at target by renaming a
// new file over it, as editors save, until the test ends. The name always
// exists.
func replaceByRenameUntilDone(t *testing.T, target string) {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = os.WriteFile(target+".tmp", []byte("saved"), 0o644)
			_ = os.Rename(target+".tmp", target)
		}
	}()
	t.Cleanup(func() {
		close(done)
		<-stopped
	})
}

// clonefileat looks the name up and then clones what it found, so a rename
// in between makes it fail as if the name were gone.
func TestFileAtClonesFileReplacedWhileCloned(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	testutil.WriteFile(t, filepath.Join(src, "main.go"), "package main")
	replaceByRenameUntilDone(t, filepath.Join(src, "main.go"))
	srcDir, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer srcDir.Close()
	dstDir, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer dstDir.Close()

	missing := 0
	for i := range 2000 {
		name := fmt.Sprintf("copy%d", i)
		if err := FileAt(srcDir, "main.go", dstDir, name); errors.Is(err, fs.ErrNotExist) {
			missing++
		} else if err != nil {
			t.Fatal(err)
		}
		_ = unix.Unlinkat(int(dstDir.Fd()), name, 0)
	}
	if missing > 0 {
		t.Errorf("%d of 2000 clones reported main.go missing while it always existed", missing)
	}
}
