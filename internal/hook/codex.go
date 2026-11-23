package hook

import (
	"io"
	"os"
	"path/filepath"
)

func codex() Agent {
	return Agent{
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
			handler: map[string]any{"type": "command", timeoutField: hookTimeout.Seconds()},
		},
	}
}

// parseCodexEvent reads a Codex event, whose cwd is the session's folder.
func parseCodexEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeEvent[eventFields](in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = fields.Cwd
	return event, nil
}
