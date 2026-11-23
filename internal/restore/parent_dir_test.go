package restore

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestParentDirRefusesNamedPipeAtOnce(t *testing.T) {
	root := testutil.TempDir(t)
	if err := syscall.Mkfifo(filepath.Join(root, "build"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()
	opened := make(chan error, 1)
	go func() {
		parent := parentDir{root: projectRoot}
		defer parent.close()
		_, err := parent.open("build/out.o")
		opened <- err
	}()
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("a named pipe opened as a directory")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a named pipe as a directory blocked")
	}
}
