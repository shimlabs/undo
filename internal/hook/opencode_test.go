package hook

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

// runPlugin loads the installed OpenCode plugin with Bun, the runtime
// OpenCode uses, and calls its tool hook once.
func runPlugin(t *testing.T, plugin, directory, tool, args string) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	script := `
const [plugin, directory, tool, args] = process.argv.slice(-4)
const { UndoPlugin } = await import(plugin)
const hooks = await UndoPlugin({ directory })
await hooks["tool.execute.before"]({ tool, sessionID: "s", callID: "c" }, { args: JSON.parse(args) })
console.log("done")
`
	out, err := exec.Command(bun, "-e", script, plugin, directory, tool, args).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "done") {
		t.Fatalf("plugin failed: %v\n%s", err, out)
	}
}

// installPlugin writes the plugin for binary. Install refuses binaries in
// temporary directories, where the test keeps its fake undo.
func installPlugin(t *testing.T, binary string) string {
	t.Helper()
	path, err := openCode.configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := openCode.installer.install(path, binary, openCode.Name); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenCodePluginSendsEvent(t *testing.T) {
	testutil.AgentHome(t)
	received := filepath.Join(t.TempDir(), "event.json")
	fake := filepath.Join(t.TempDir(), "undo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat > '"+received+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := installPlugin(t, fake)
	project := t.TempDir()

	runPlugin(t, plugin, project, "edit", `{"filePath":"`+project+`/a.go","oldString":"x","newString":"y"}`)

	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatalf("undo was not run: %v", err)
	}
	event, err := parseOpenCodeEvent(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if event.ProjectDir != project || event.Dir != project || event.Session != "s" || event.ToolName != "edit" || event.FilePath != project+"/a.go" {
		t.Fatalf("event = %+v", event)
	}
}

func TestOpenCodePluginIgnoresReadOnlyTools(t *testing.T) {
	testutil.AgentHome(t)
	received := filepath.Join(t.TempDir(), "event.json")
	fake := filepath.Join(t.TempDir(), "undo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat > '"+received+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := installPlugin(t, fake)

	runPlugin(t, plugin, t.TempDir(), "read", `{"filePath":"/a.go"}`)

	if _, err := os.Stat(received); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("undo ran for a tool that does not change files")
	}
}

func TestOpenCodePluginNeverBlocks(t *testing.T) {
	testutil.AgentHome(t)
	plugin := installPlugin(t, filepath.Join(t.TempDir(), "missing-undo"))

	runPlugin(t, plugin, t.TempDir(), "bash", `{"command":"rm -rf build"}`)
}
