package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestRunFailsLikeAShell(t *testing.T) {
	root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "script.sh"), "#!/bin/sh\n")
	tests := []struct {
		args     []string
		exitCode int
		want     string
	}{
		{[]string{"run", "--", "no-such-command-anywhere"}, 127, "Command not found: no-such-command-anywhere."},
		{[]string{"run", "--", "./missing.sh"}, 127, "No such file: ./missing.sh."},
		{[]string{"run", "--", "./script.sh"}, 126, "Run chmod +x ./script.sh to make it executable."},
	}
	for _, test := range tests {
		code, out := run(t, root, "", test.args...)
		if code != test.exitCode || !strings.Contains(out, test.want) {
			t.Errorf("undo %s: exit %d, want %d and %q\n%s", strings.Join(test.args, " "), code, test.exitCode, test.want, out)
		}
	}
	if list := mustRun(t, root, "list"); strings.Contains(list, "script.sh") {
		t.Fatalf("a command that cannot run got a checkpoint:\n%s", list)
	}
}
