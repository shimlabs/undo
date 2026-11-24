package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ExFATDir returns a new folder on an exFAT disk image, where macOS keeps
// each file's extended attributes in a "._" file beside it.
func ExFATDir(tb testing.TB) string {
	tb.Helper()
	return VolumeDir(tb, "ExFAT")
}

// VolumeDir returns a new folder named project at the top of a disk image
// of filesystem, as hdiutil names it, such as "APFS" or "ExFAT". The image
// is detached and deleted when the test ends.
func VolumeDir(tb testing.TB, filesystem string) string {
	tb.Helper()
	work := TempDir(tb)
	image := filepath.Join(work, "volume.dmg")
	mountPoint := filepath.Join(work, "volume")
	if out, err := exec.CommandContext(tb.Context(), "hdiutil", "create", "-quiet", "-size", "32m", "-fs", filesystem, "-volname", "undotest", image).CombinedOutput(); err != nil {
		tb.Skipf("cannot make a %s disk image: %v: %s", filesystem, err, out)
	}
	if out, err := exec.CommandContext(tb.Context(), "hdiutil", "attach", "-quiet", "-nobrowse", "-mountpoint", mountPoint, image).CombinedOutput(); err != nil {
		tb.Skipf("cannot attach a %s disk image: %v: %s", filesystem, err, out)
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
