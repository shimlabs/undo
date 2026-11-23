package hook

import (
	"os"
	"path/filepath"
)

var claude = Agent{
	Name:  "claude",
	Title: "Claude Code",
	Note:  "Claude Code now takes a checkpoint before every Bash, Edit and Write call.",
	configPath: func() (string, error) {
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, "settings.json"), nil
		}
		return homePath(".claude", "settings.json")
	},
	event: "PreToolUse",
	entry: func(command string) map[string]any {
		return map[string]any{
			"matcher": "Bash|Edit|Write|MultiEdit|NotebookEdit",
			"hooks":   []any{map[string]any{"type": "command", "command": command}},
		}
	},
}
