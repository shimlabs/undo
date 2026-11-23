package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/watch"
)

func TestRestoreRefusesToAskWithoutTerminal(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	testutil.WriteFile(t, filepath.Join(root, "f.txt"), "changed")
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pipeReader.Close() })
	pipeWriter.Close()
	var out bytes.Buffer
	code := Run([]string{"to", "1"}, Env{
		Dir: root, Stdin: pipeReader, Stdout: &out, Stderr: &out,
		StoreHome: defaultStoreHome(t), SocketDir: watch.DefaultSocketDir(),
	})

	if code != 1 || !strings.Contains(out.String(), "Run undo to 1 --yes to restore without asking.") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if data, err := os.ReadFile(filepath.Join(root, "f.txt")); err != nil || string(data) != "changed" {
		t.Fatalf("f.txt = %q, %v, want it untouched", data, err)
	}
}

func TestRestoreWithNothingToDoTakesNoCheckpoint(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")

	out := mustRun(t, root, "--yes")
	if !strings.Contains(out, "Nothing to restore") || !strings.Contains(out, "Run undo list to pick an earlier checkpoint") {
		t.Fatalf("undo --yes with nothing to do:\n%s", out)
	}
	if list := mustRun(t, root, "list"); strings.Contains(list, "  2  ") {
		t.Fatalf("a restore with nothing to do took a checkpoint:\n%s", list)
	}
}

func TestRestoreTotalsAddUpRows(t *testing.T) {
	root := newProject(t)
	for _, rel := range []string{"src/a.ts", "src/b.ts", "src/lib/c.ts"} {
		testutil.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), "code")
	}
	mustRun(t, root, "checkpoint")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "new")
	mustRun(t, root, "checkpoint")

	out := mustRun(t, root, "diff", "1")
	for _, want := range []string{"restore  src/       4 paths", "remove   notes.txt", "Run undo to 1 to restore these 5 paths."} {
		if !strings.Contains(out, want) {
			t.Errorf("diff does not show %q:\n%s", want, out)
		}
	}
	if out := mustRun(t, root, "to", "1", "--yes"); !strings.Contains(out, "Restored 5 paths.") {
		t.Errorf("restore:\n%s", out)
	}
}

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
