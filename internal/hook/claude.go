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
	installer: hookGroups{
		event:   "PreToolUse",
		matcher: "Bash|Edit|Write|MultiEdit|NotebookEdit",
		handler: map[string]any{"type": "command", "timeout": 30},
	},
	// Cursor also runs Claude Code's hooks. When undo's Cursor hook is
	// installed, it covers the event, and a second checkpoint would only
	// repeat the first.
	isHandledElsewhere: func(event preToolUseInput) bool {
		return event.CursorVersion != "" && cursor.IsInstalled()
	},
}
