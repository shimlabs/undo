package cli

import (
	"path/filepath"
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

func TestInvalidAgentSettingsSayWhereAndWhatToDo(t *testing.T) {
	home := testutil.AgentHome(t)
	testutil.WriteFile(t, filepath.Join(home, ".gemini", "settings.json"), "{bad")

	code, out := run(t, t.TempDir(), "", "install", "gemini")
	if code != 1 || !strings.Contains(out, "Error: ~/.gemini/settings.json is not valid JSON (line 1, column 2), so undo left it unchanged.\nFix the file, then run undo install gemini again.\n") {
		t.Fatalf("install: exit %d:\n%s", code, out)
	}
	_, out = run(t, newProject(t), "", "status")
	if !strings.Contains(out, "Gemini CLI: ~/.gemini/settings.json is not valid JSON, so undo cannot tell whether its hook is installed") {
		t.Fatalf("status:\n%s", out)
	}
}
