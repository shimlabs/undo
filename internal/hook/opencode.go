package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

var openCode = Agent{
	Name:  "opencode",
	Title: "OpenCode",
	Note: "OpenCode now takes a checkpoint before every shell command and file edit.\n" +
		"Restart OpenCode to load the plugin.",
	configPath: func() (string, error) {
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			return filepath.Join(dir, "opencode", "plugins", "undo.js"), nil
		}
		return homePath(".config", "opencode", "plugins", "undo.js")
	},
	plugin: openCodePlugin,
}

// openCodePlugin returns a plugin that sends each file-changing tool call to
// "undo hook opencode" in the shape the other agents use.
func openCodePlugin(binary string) []byte {
	quoted, _ := json.Marshal(binary)
	return fmt.Appendf(nil, `// Written by "undo install opencode". Remove it with "undo uninstall opencode".
const UNDO = %s
const FILE_TOOLS = new Set(["bash", "edit", "write", "apply_patch"])

export const UndoPlugin = async ({ directory }) => ({
  "tool.execute.before": async (input, output) => {
    if (!FILE_TOOLS.has(input.tool)) return
    const args = output.args ?? {}
    const event = {
      cwd: args.workdir ?? directory,
      tool_name: input.tool,
      tool_input: { command: args.command ?? args.patchText, file_path: args.filePath },
    }
    // OpenCode fails the tool call when this hook throws, and undo must never
    // stop the agent, so every error is ignored.
    try {
      const child = Bun.spawn([UNDO, "hook", "opencode"], {
        cwd: directory,
        stdin: new Blob([JSON.stringify(event)]),
        stdout: "ignore",
        stderr: "ignore",
      })
      const timer = setTimeout(() => child.kill(), 30_000)
      await child.exited
      clearTimeout(timer)
    } catch {}
  },
})
`, quoted)
}
