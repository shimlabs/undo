package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func run(t *testing.T, dir string, stdin string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(args, Env{Dir: dir, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &out})
	return code, out.String()
}

func TestRunCheckpointsThenRuns(t *testing.T) {
	root := newProject(t)

	code, out := run(t, root, "", "run", "--", "sh", "-c", "rm f.txt")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(err) {
		t.Fatal("command did not run")
	}

	code, out = run(t, root, "", "to", "1", "--yes")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(data) != "keep me" {
		t.Fatalf("f.txt = %q after restore", data)
	}
}

func TestRunReturnsCommandExitCode(t *testing.T) {
	root := newProject(t)

	code, _ := run(t, root, "", "run", "--", "sh", "-c", "exit 7")
	if code != 7 {
		t.Fatalf("exit %d, want 7", code)
	}
}

func TestUndoRestoresLatest(t *testing.T) {
	root := newProject(t)
	if code, out := run(t, root, "", "checkpoint", "--label", "rm f.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	os.Remove(filepath.Join(root, "f.txt"))

	code, out := run(t, root, "y\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); err != nil {
		t.Fatalf("f.txt not restored: %s", out)
	}
}

func TestUndoAsksBeforeRestoring(t *testing.T) {
	root := newProject(t)
	run(t, root, "", "checkpoint")
	os.Remove(filepath.Join(root, "f.txt"))

	code, out := run(t, root, "n\n")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(err) {
		t.Fatal("restored without confirmation")
	}
}

func TestDiffListsPaths(t *testing.T) {
	root := newProject(t)
	run(t, root, "", "checkpoint")
	os.Remove(filepath.Join(root, "f.txt"))

	code, out := run(t, root, "", "diff", "1")
	if code != 0 || !strings.Contains(out, "f.txt") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestListShowsLabels(t *testing.T) {
	root := newProject(t)
	run(t, root, "", "checkpoint", "--label", "npm run build")

	code, out := run(t, root, "", "list")
	if code != 0 || !strings.Contains(out, "npm run build") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestUsageExitCode(t *testing.T) {
	root := newProject(t)
	if code, _ := run(t, root, "", "frobnicate"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if code, _ := run(t, root, "", "to"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestDiffGroupsDirectories(t *testing.T) {
	root := newProject(t)
	for i := range 30 {
		path := filepath.Join(root, "big", "pkg", "f"+strconv.Itoa(i)+".txt")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("x"), 0o644)
	}
	run(t, root, "", "checkpoint")
	os.RemoveAll(filepath.Join(root, "big"))
	os.Remove(filepath.Join(root, "f.txt"))

	_, out := run(t, root, "", "diff", "1")

	if !strings.Contains(out, "restore  big/  (32 paths)") || !strings.Contains(out, "restore  f.txt\n") {
		t.Fatalf("output not grouped:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > 3 {
		t.Fatalf("%d lines, want at most 3:\n%s", lines, out)
	}
}

func TestInstallClaudeWritesSettings(t *testing.T) {
	root := newProject(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	code, out := run(t, root, "", "install", "claude")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	data, err := os.ReadFile(filepath.Join(config, "settings.json"))
	if err != nil || !strings.Contains(string(data), "hook claude") {
		t.Fatalf("settings = %s, %v", data, err)
	}

	if code, out := run(t, root, "", "uninstall", "claude"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	data, _ = os.ReadFile(filepath.Join(config, "settings.json"))
	if strings.Contains(string(data), "hook claude") {
		t.Fatalf("hook still installed: %s", data)
	}
}

func TestHookNeverBlocksTheAgent(t *testing.T) {
	root := newProject(t)

	code, out := run(t, root, "not json", "hook", "claude")
	if code != 0 || out != "" {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestInstallUnknownAgent(t *testing.T) {
	root := newProject(t)
	if code, _ := run(t, root, "", "install", "notepad"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
