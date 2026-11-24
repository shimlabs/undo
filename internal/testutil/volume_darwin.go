package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
		if err := detach(detachContext, mountPoint); err != nil {
			tb.Error(err)
		}
	})
	dir := filepath.Join(mountPoint, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// detach detaches the disk image mounted at mountPoint. Spotlight and other
// system services can hold a new volume busy for a few seconds, even against
// -force, so it tries again for a while.
func detach(ctx context.Context, mountPoint string) error {
	var err error
	for range 20 {
		var out []byte
		if out, err = exec.CommandContext(ctx, "hdiutil", "detach", "-force", mountPoint).CombinedOutput(); err == nil {
			return nil
		}
		err = fmt.Errorf("detach %s: %w: %s", mountPoint, err, out)
		time.Sleep(500 * time.Millisecond)
	}
	return err
}
