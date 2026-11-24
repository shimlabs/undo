package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

// A project at the top of a volume whose top folder cannot be written keeps
// its store under the home folder, on another volume, where nothing can be
// cloned from the project although APFS clones. --copy is then the way to
// protect it.
func TestCheckpointCopyFlagWorksWhereStoreIsOnAnotherVolume(t *testing.T) {
	root := testutil.VolumeDir(t, "APFS")
	t.Setenv("UNDO_HOME", t.TempDir())
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	volume := filepath.Dir(root)
	if err := os.Chmod(volume, 0o555); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, volume, 0o755)

	code, out := run(t, root, "", "checkpoint")
	if code != 1 || !strings.Contains(out, "undo checkpoint --copy") {
		t.Fatalf("checkpoint: exit %d, want 1 and a pointer to --copy: %s", code, out)
	}
	code, out = run(t, root, "", "checkpoint", "--copy")
	if code != 0 || !strings.Contains(out, "Checkpoints of this project now keep full copies") {
		t.Fatalf("checkpoint --copy: exit %d: %s", code, out)
	}
}
