package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestOpenCodePluginSendsEvent(t *testing.T) {
	agentHome(t)
	received := filepath.Join(t.TempDir(), "event.json")
	fake := filepath.Join(t.TempDir(), "undo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat > '"+received+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugin, err := openCode.Install(fake)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()

	runPlugin(t, plugin, project, "edit", `{"filePath":"`+project+`/a.go","oldString":"x","newString":"y"}`)

	var event preToolUseInput
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatalf("undo was not run: %v", err)
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Cwd != project || event.ToolName != "edit" || event.ToolInput.FilePath != project+"/a.go" {
		t.Fatalf("event = %+v", event)
	}
}

func TestOpenCodePluginIgnoresReadOnlyTools(t *testing.T) {
	agentHome(t)
	received := filepath.Join(t.TempDir(), "event.json")
	fake := filepath.Join(t.TempDir(), "undo")
	os.WriteFile(fake, []byte("#!/bin/sh\ncat > '"+received+"'\n"), 0o755)
	plugin, _ := openCode.Install(fake)

	runPlugin(t, plugin, t.TempDir(), "read", `{"filePath":"/a.go"}`)

	if _, err := os.Stat(received); !os.IsNotExist(err) {
		t.Fatal("undo ran for a tool that does not change files")
	}
}

func TestOpenCodePluginNeverBlocks(t *testing.T) {
	agentHome(t)
	plugin, _ := openCode.Install(filepath.Join(t.TempDir(), "missing-undo"))

	runPlugin(t, plugin, t.TempDir(), "bash", `{"command":"rm -rf build"}`)
}
