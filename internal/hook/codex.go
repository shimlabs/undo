package hook

import (
	"os"
	"path/filepath"
)

var codex = Agent{
	Name:  "codex",
	Title: "Codex",
	Note: "Codex runs new hooks only after you trust them: open Codex, run /hooks and trust the undo hook.\n" +
		"Then Codex takes a checkpoint before every shell command and patch.",
	parseEvent: parseCodexEvent,
	configPath: func() (string, error) {
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "hooks.json"), nil
		}
		return homePath(".codex", "hooks.json")
	},
	installer: hookGroups{
		event: "PreToolUse",
		// Codex reports shell commands as Bash and makes file edits with
		// apply_patch.
		matcher: "Bash|apply_patch",
		handler: map[string]any{"type": "command", "timeout": 30},
	},
}
