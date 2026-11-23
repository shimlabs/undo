package hook

import (
	"cmp"
	"io"
	"os"
	"path/filepath"
)

func claude() Agent {
	return Agent{
		Name:       "claude",
		Title:      "Claude Code",
		Note:       "Claude Code now takes a checkpoint before every Bash, Edit and Write call.",
		parseEvent: parseClaudeEvent,
		configPath: func() (string, error) {
			if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
				return filepath.Join(dir, "settings.json"), nil
			}
			return homePath(".claude", "settings.json")
		},
		installer: hookGroups{
			event:   "PreToolUse",
			matcher: "Bash|Edit|Write|MultiEdit|NotebookEdit",
			handler: map[string]any{"type": "command", timeoutField: hookTimeout.Seconds()},
		},
		// Cursor also runs Claude Code's hooks. When undo's Cursor hook is
		// installed, it covers the event, and a second checkpoint would only
		// repeat the first.
		isHandledElsewhere: func(event hookEvent) bool {
			isInstalled, err := cursor().IsInstalled()
			return event.IsFromCursor && err == nil && isInstalled
		},
	}
}

// parseClaudeEvent reads a Claude Code event. Claude Code passes the folder
// the session started in as CLAUDE_PROJECT_DIR. Cursor runs Claude Code's
// hooks too, with its own fields.
func parseClaudeEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeEvent[cursorFields](in)
	if err != nil {
		return hookEvent{}, err
	}
	if fields.CursorVersion != "" {
		return fields.event(), nil
	}
	event := fields.eventFields.event()
	event.ProjectDir = cmp.Or(os.Getenv("CLAUDE_PROJECT_DIR"), fields.Cwd)
	return event, nil
}
