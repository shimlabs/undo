package cli

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

func TestWatcherStopErrorReadsLikeOtherErrors(t *testing.T) {
	err := fmt.Errorf("%w", &fs.PathError{Op: "openat", Path: ".undoignore", Err: syscall.EACCES})
	if got, want := watcherStoppedError(err).Error(), "the watcher stopped: cannot open .undoignore: permission denied"; got != want {
		t.Fatalf("logged %q, want %q", got, want)
	}
}
