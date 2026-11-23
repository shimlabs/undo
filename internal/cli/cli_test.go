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

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/watch"
)

func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func defaultStoreHome(t *testing.T) string {
	t.Helper()
	storeHome, err := store.DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	return storeHome
}

// mustRun runs undo like run and fails the test if it exits with an error.
func mustRun(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	code, out := run(t, dir, stdin, args...)
	if code != 0 {
		t.Fatalf("undo %v: exit %d: %s", args, code, out)
	}
	return out
}

// run runs undo with args in dir, with the store home and socket directory
// taken from the environment, as the binary does.
func run(t *testing.T, dir string, stdin string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(args, Env{
		Dir:        dir,
		Stdin:      strings.NewReader(stdin),
		Stdout:     &out,
		Stderr:     &out,
		StoreHome:  defaultStoreHome(t),
		SocketDir:  watch.DefaultSocketDir(),
		Executable: testutil.PrivateBinary(t, "bin"),
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
	if err := os.Remove(filepath.Join(root, "f.txt")); err != nil {
		t.Fatal(err)
	}

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
	mustRun(t, root, "", "checkpoint")
	if err := os.Remove(filepath.Join(root, "f.txt")); err != nil {
		t.Fatal(err)
	}

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
	mustRun(t, root, "", "checkpoint")
	if err := os.Remove(filepath.Join(root, "f.txt")); err != nil {
		t.Fatal(err)
	}

	code, out := run(t, root, "", "diff", "1")
	if code != 0 || !strings.Contains(out, "f.txt") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestListShowsLabels(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "", "checkpoint", "--label", "npm run build")

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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		testutil.WriteFile(t, path, "x")
	}
	mustRun(t, root, "", "checkpoint")
	if err := os.RemoveAll(filepath.Join(root, "big")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "f.txt")); err != nil {
		t.Fatal(err)
	}

	_, out := run(t, root, "", "diff", "1")

	if !strings.Contains(out, "restore  big/") || !strings.Contains(out, "32 paths") || !strings.Contains(out, "restore  f.txt\n") {
		t.Fatalf("output not grouped:\n%s", out)
	}
	if rows := strings.Count(out, "  restore  "); rows != 2 {
		t.Fatalf("%d restore rows, want 2:\n%s", rows, out)
	}
}

func TestInstallClaudeWritesSettings(t *testing.T) {
	root := newProject(t)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	code, out := run(t, root, "", "install", "claude")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil || !strings.Contains(string(data), "hook claude") {
		t.Fatalf("settings = %s, %v", data, err)
	}

	if code, out := run(t, root, "", "uninstall", "claude"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	data, _ = os.ReadFile(filepath.Join(configDir, "settings.json"))
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

func TestHookForUnknownAgentLogsAndTakesNoCheckpoint(t *testing.T) {
	root := newProject(t)
	storeHome := defaultStoreHome(t)
	event := `{"cwd":` + strconv.Quote(root) + `,"tool_name":"Bash","tool_input":{"command":"ls"}}`
	if code, out := run(t, root, event, "hook", "no-such-agent"); code != 0 || out != "" {
		t.Fatalf("exit %d, output %q", code, out)
	}
	if _, err := store.Open(storeHome, root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hook for an unknown agent opened a store: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(storeHome, "errors.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "no-such-agent") {
		t.Fatalf("errors.log = %q, want the unknown agent named", data)
	}
}

func TestHelpListsEveryAgent(t *testing.T) {
	_, out := run(t, t.TempDir(), "", "help")
	for _, agent := range hook.Agents() {
		if !strings.Contains(out, agent.Name) {
			t.Errorf("help does not mention %s", agent.Name)
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
		"Checkpoints  1, the oldest ",
		"Disk used    ",
		"not shared with the project, limit 5.0 GB",
		"Watcher      not running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
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
	projectStore, err := store.Create(defaultStoreHome(t), root)
	if err != nil {
		t.Fatal(err)
	}
	projectStore.LogError(errors.New("first failure"))
	projectStore.LogError(errors.New("disk full"))

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
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")

	code, out := run(t, root, "", "checkpoint")
	if code != 1 || !strings.Contains(out, "undo checkpoint --copy") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestStatusShowsInstalledHooks(t *testing.T) {
	root := newProject(t)
	testutil.AgentHome(t)
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
	parent := testutil.TempDir(t)
	root := filepath.Join(parent, "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	_, out := run(t, parent, "", "list")
	if !strings.Contains(out, "was deleted. Bring it back with:\n  undo to 1 --project") {
		t.Fatalf("list in the parent does not point to the deleted project:\n%s", out)
	}
	if code, out := run(t, parent, "", "to", "1", "--yes", "--project", root); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); string(data) != "package main" {
		t.Fatalf("main.go = %q after restoring the deleted project", data)
	}
}

func TestDeletedProjectHintIsQuotedForTheShell(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	parent := testutil.TempDir(t)
	root := filepath.Join(parent, "it's $(app)")
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	_, out := run(t, parent, "", "list")
	want := "--project '" + strings.ReplaceAll(root, "'", `'\''`) + "'"
	if !strings.Contains(out, want) {
		t.Fatalf("hint is not quoted for the shell, want %s in:\n%s", want, out)
	}
}

func TestDeletedProjectHintWithControlCharacterIsNoCommand(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	parent := testutil.TempDir(t)
	root := filepath.Join(parent, "x$(app)\t")
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	_, out := run(t, parent, "", "list")
	if !strings.Contains(out, "was deleted") || strings.Contains(out, "--project \"") || strings.Contains(out, "--project '") {
		t.Fatalf("hint for a path with a control character:\n%s", out)
	}
}

func TestStatusEscapesExcludePatterns(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	root := testutil.TempDir(t)
	testutil.WriteFile(t, filepath.Join(root, ".undoignore"), "x\x1b]0;pwned\x07\n")

	_, out := run(t, root, "", "status")
	if strings.Contains(out, "\x1b") {
		t.Fatalf("status printed a raw escape sequence:\n%q", out)
	}
}

func TestStatusShowsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	root := newProject(t)
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "pgdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })
	mustRun(t, root, "", "checkpoint")

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
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("UNDO_HOME", t.TempDir())
	t.Setenv("UNDO_SOCKET_DIR", t.TempDir())
	root := filepath.Join(home, "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "main.go"), "package main")
	if code, out := run(t, root, "", "checkpoint"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	code, out := run(t, home, "", "list")
	if code != 0 || !strings.Contains(out, "was deleted. Bring it back with:\n  undo to 1 --project") {
		t.Fatalf("exit %d, list in the home directory:\n%s", code, out)
	}
}
