package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/store"
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

// privateBinary returns the path of an undo binary in a directory only the
// test user can write to, outside the temporary directory, which install
// refuses.
func privateBinary(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(parent, "tmp"))
	os.MkdirAll(filepath.Join(parent, "tmp"), 0o700)
	os.MkdirAll(filepath.Join(parent, "bin"), 0o755)
	return filepath.Join(parent, "bin", "undo")
}

// run runs undo with args in dir, with the store home and socket directory
// taken from the environment, as the binary does.
func storeHomeFor(t *testing.T) string {
	t.Helper()
	storeHome, err := store.DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	return storeHome
}

func run(t *testing.T, dir string, stdin string, args ...string) (int, string) {
	t.Helper()
	storeHome, err := store.DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := Run(args, Env{
		Dir:        dir,
		Stdin:      strings.NewReader(stdin),
		Stdout:     &out,
		Stderr:     &out,
		StoreHome:  storeHome,
		Executable: privateBinary(t),
	})
	return code, out.String()
}

func TestRunCheckpointsThenRuns(t *testing.T) {
	root := newProject(t)

	code, out := run(t, root, "", "run", "--", "sh", "-c", "rm f.txt")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !errors.Is(err, fs.ErrNotExist) {
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
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !errors.Is(err, fs.ErrNotExist) {
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

	for _, args := range [][]string{{"hook", "claude"}, {"hook", "no-such-agent"}, {"hook"}} {
		code, out := run(t, root, "not json", args...)
		if code != 0 || out != "" {
			t.Fatalf("%v: exit %d, output %q", args, code, out)
		}
	}
}

func TestInstallUnknownAgent(t *testing.T) {
	root := newProject(t)
	if code, _ := run(t, root, "", "install", "notepad"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestStatusShowsDiskUseAndWatcher(t *testing.T) {
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}

	code, out := run(t, root, "", "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{
		"Checkpoints  1 (oldest ",
		"Disk used    ",
		"not shared with the project, limit 5.0 GB",
		"Watcher      not running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for size, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 kB", 2_340_000: "2.3 MB", 5_000_000_000: "5.0 GB"} {
		if got := formatBytes(size); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestCheckpointCopyFlagSavesCopyMode(t *testing.T) {
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())

	if code, out := run(t, root, "", "checkpoint", "--copy"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	_, out := run(t, root, "", "status")
	if !strings.Contains(out, "Storage      full copies") {
		t.Fatalf("status does not show copy mode:\n%s", out)
	}
}

func TestStatusShowsLastError(t *testing.T) {
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	s, err := store.Create(storeHomeFor(t), root)
	if err != nil {
		t.Fatal(err)
	}
	s.LogError(errors.New("first failure"))
	s.LogError(errors.New("disk full"))

	_, out := run(t, root, "", "status")
	if !strings.Contains(out, "Last error   ") || !strings.Contains(out, " disk full\n") {
		t.Fatalf("status does not show the last hook error:\n%s", out)
	}
}

func TestInstallCodexExplainsTrust(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())

	code, out := run(t, t.TempDir(), "", "install", "codex")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "/hooks") {
		t.Fatalf("install output does not explain the trust step:\n%s", out)
	}
}

func TestHookCodexTakesCheckpoint(t *testing.T) {
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	input := `{"cwd":"` + root + `","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Update File: f.txt\n*** End Patch"}}`

	if code, out := run(t, root, input, "hook", "codex"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	_, out := run(t, root, "", "list")
	if !strings.Contains(out, "apply_patch f.txt") {
		t.Fatalf("list does not show the Codex checkpoint:\n%s", out)
	}
}

func TestRefusalExplainsCopyMode(t *testing.T) {
	plain := os.Getenv("UNDO_TEST_PLAIN_DIR")
	if plain == "" {
		t.Skip("UNDO_TEST_PLAIN_DIR is not set")
	}
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := os.MkdirTemp(plain, "project-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)

	code, out := run(t, root, "", "checkpoint")
	if code != 1 || !strings.Contains(out, "undo checkpoint --copy") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestStatusShowsInstalledHooks(t *testing.T) {
	root := newProject(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())

	_, out := run(t, root, "", "status")
	if !strings.Contains(out, "Hooks        none yet, run: undo install <agent>") {
		t.Fatalf("status without hooks:\n%s", out)
	}

	for _, agent := range []string{"cursor", "opencode"} {
		if code, out := run(t, root, "", "install", agent); code != 0 {
			t.Fatalf("install %s: exit %d: %s", agent, code, out)
		}
	}
	_, out = run(t, root, "", "status")
	if !strings.Contains(out, "Hooks        Cursor, OpenCode") {
		t.Fatalf("status with hooks:\n%s", out)
	}
}

func TestUndoFindsDeletedProject(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(parent, "app")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	os.RemoveAll(root)

	_, out := run(t, parent, "", "list")
	if !strings.Contains(out, "was deleted. Bring it back with: undo to 1 --project") {
		t.Fatalf("list in the parent does not point to the deleted project:\n%s", out)
	}
	if code, out := run(t, parent, "", "to", "1", "--yes", "--project", root); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); string(data) != "package main" {
		t.Fatalf("main.go = %q after restoring the deleted project", data)
	}
}

func TestPrintableEscapesControlCharacters(t *testing.T) {
	if got := printable("rm -rf build"); got != "rm -rf build" {
		t.Fatalf("printable changed plain text: %q", got)
	}
	for _, text := range []string{"echo \x1b]0;pwned\x07", "a\x9bb", "invoice\u202egpj.exe"} {
		if got := printable(text); got == text {
			t.Errorf("printable(%q) left the text unescaped", text)
		}
	}
}

func TestStatusShowsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	os.MkdirAll(filepath.Join(root, "pgdata"), 0o755)
	os.Chmod(filepath.Join(root, "pgdata"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })
	run(t, root, "", "checkpoint")

	_, out := run(t, root, "", "status")
	if !strings.Contains(out, "Unreadable   pgdata") {
		t.Fatalf("status does not report the unreadable directory:\n%s", out)
	}
}

func TestErrorsAreEscaped(t *testing.T) {
	root := newProject(t)
	code, out := run(t, root, "", "to", "1", "--project", filepath.Join(root, "a\x1b]0;pwned\x07"))
	if code == 0 || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestListInHomeFindsDeletedProject(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("UNDO_HOME", t.TempDir())
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	root := filepath.Join(home, "app")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	os.RemoveAll(root)

	code, out := run(t, home, "", "list")
	if code != 0 || !strings.Contains(out, "was deleted. Bring it back with: undo to 1 --project") {
		t.Fatalf("exit %d, list in the home directory:\n%s", code, out)
	}
}
