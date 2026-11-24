package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ExFATDir returns a new folder on an exFAT disk image, where macOS keeps
// each file's extended attributes in a "._" file beside it. The image is
// detached and deleted when the test ends.
func ExFATDir(tb testing.TB) string {
	tb.Helper()
	work := TempDir(tb)
	image := filepath.Join(work, "volume.dmg")
	mountPoint := filepath.Join(work, "volume")
	if out, err := exec.CommandContext(tb.Context(), "hdiutil", "create", "-quiet", "-size", "32m", "-fs", "ExFAT", "-volname", "undotest", image).CombinedOutput(); err != nil {
		tb.Skipf("cannot make an exFAT disk image: %v: %s", err, out)
	}
	if out, err := exec.CommandContext(tb.Context(), "hdiutil", "attach", "-quiet", "-nobrowse", "-mountpoint", mountPoint, image).CombinedOutput(); err != nil {
		tb.Skipf("cannot attach an exFAT disk image: %v: %s", err, out)
	}
	// The test's context is canceled before cleanups run.
	detachContext := context.WithoutCancel(tb.Context())
	tb.Cleanup(func() {
		if out, err := exec.CommandContext(detachContext, "hdiutil", "detach", "-force", mountPoint).CombinedOutput(); err != nil {
			tb.Errorf("detach %s: %v: %s", mountPoint, err, out)
		}
	})
	dir := filepath.Join(mountPoint, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	return dir
}
