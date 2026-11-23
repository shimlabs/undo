package cli

import (
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestInstallAndUninstallSayWhenNothingChanges(t *testing.T) {
	testutil.AgentHome(t)
	root := newProject(t)
	mustRun(t, root, "install", "claude")
	if out := mustRun(t, root, "install", "claude"); !strings.Contains(out, "is already installed") {
		t.Errorf("second install:\n%s", out)
	}
	mustRun(t, root, "uninstall", "claude")
	if out := mustRun(t, root, "uninstall", "claude"); !strings.Contains(out, "is not installed") {
		t.Errorf("second uninstall:\n%s", out)
	}
}
