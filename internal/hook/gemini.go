package hook

import (
	"os"
	"path/filepath"
)

var gemini = Agent{
	Name:  "gemini",
	Title: "Gemini CLI",
	Note: "Gemini CLI now takes a checkpoint before every shell command and file edit.\n" +
		"It runs hooks only in folders you trust.",
	configPath: func() (string, error) {
		if dir := os.Getenv("GEMINI_CLI_HOME"); dir != "" {
			return filepath.Join(dir, ".gemini", "settings.json"), nil
		}
		return homePath(".gemini", "settings.json")
	},
	event: "BeforeTool",
	entry: func(command string) map[string]any {
		return map[string]any{
			// Gemini CLI matches with an unanchored regex.
			"matcher": "^(run_shell_command|write_file|replace)$",
			"hooks": []any{map[string]any{
				"name":    "undo",
				"type":    "command",
				"command": command,
				// Gemini CLI counts hook timeouts in milliseconds.
				"timeout": 30000,
			}},
		}
	},
}
