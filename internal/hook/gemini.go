package hook

import (
	"cmp"
	"io"
	"os"
	"path/filepath"
)

var gemini = Agent{
	Name:  "gemini",
	Title: "Gemini CLI",
	Note: "Gemini CLI now takes a checkpoint before every shell command and file edit.\n" +
		"It runs hooks only in folders you trust.",
	parseEvent: parseGeminiEvent,
	configPath: func() (string, error) {
		if dir := os.Getenv("GEMINI_CLI_HOME"); dir != "" {
			return filepath.Join(dir, ".gemini", "settings.json"), nil
		}
		return homePath(".gemini", "settings.json")
	},
	installer: hookGroups{
		event: "BeforeTool",
		// Gemini CLI matches with an unanchored regex.
		matcher: "^(run_shell_command|write_file|replace)$",
		handler: map[string]any{
			"name": "undo",
			"type": "command",
			// Gemini CLI counts hook timeouts in milliseconds.
			"timeout": hookTimeout.Milliseconds(),
		},
	},
}

// parseGeminiEvent reads a Gemini CLI event. Gemini CLI passes the project
// folder as GEMINI_PROJECT_DIR.
func parseGeminiEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeEvent[eventFields](in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = cmp.Or(os.Getenv("GEMINI_PROJECT_DIR"), fields.Cwd)
	return event, nil
}
