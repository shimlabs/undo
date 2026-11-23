package hook

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func openCode() Agent {
	return Agent{
		Name:  "opencode",
		Title: "OpenCode",
		Note: "OpenCode now takes a checkpoint before every shell command and file edit.\n" +
			"Restart OpenCode to load the plugin.",
		parseEvent: parseOpenCodeEvent,
		configPath: func() (string, error) {
			if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
				return filepath.Join(dir, "opencode", "plugins", "undo.js"), nil
			}
			return homePath(".config", "opencode", "plugins", "undo.js")
		},
		installer: pluginFile{content: openCodePlugin},
	}
}

// pluginHeader starts every plugin file undo writes, so undo can tell its own
// file from one the user wrote.
const pluginHeader = `// Written by "undo install opencode". Remove it with "undo uninstall opencode".`

// openCodePlugin returns a plugin that sends each file-changing tool call to
// "undo hook opencode" in the shape the other agents use.
func openCodePlugin(binary string) ([]byte, error) {
	quoted, err := json.Marshal(binary)
	if err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, `%s
const UNDO = %s
const FILE_TOOLS = new Set(["bash", "edit", "write", "apply_patch"])

export const UndoPlugin = async ({ directory }) => ({
  "tool.execute.before": async (input, output) => {
    if (!FILE_TOOLS.has(input.tool)) return
    const args = output.args ?? {}
    const event = {
      project_dir: directory,
      cwd: args.workdir ?? directory,
      session_id: input.sessionID,
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
      const timer = setTimeout(() => child.kill(), %d)
      await child.exited
      clearTimeout(timer)
    } catch {}
  },
})
`, pluginHeader, quoted, hookTimeout.Milliseconds()), nil
}

// openCodeFields are the fields undo's OpenCode plugin sends.
type openCodeFields struct {
	eventFields

	ProjectDir string `json:"project_dir"`
}

// parseOpenCodeEvent reads an event from undo's OpenCode plugin, which sends
// the folder OpenCode was started in as project_dir.
func parseOpenCodeEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeEvent[openCodeFields](in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = cmp.Or(fields.ProjectDir, fields.Cwd)
	return event, nil
}
