package hook

var cursor = Agent{
	Name:  "cursor",
	Title: "Cursor",
	Note: "Cursor now takes a checkpoint before every shell command, edit and delete.\n" +
		"Restart Cursor agent sessions that are already running in the terminal.",
	configPath: func() (string, error) {
		return homePath(".cursor", "hooks.json")
	},
	event: "preToolUse",
	entry: func(command string) map[string]any {
		return map[string]any{
			"command": command,
			// Cursor matches with an unanchored regex, and a bare Write would
			// also match WriteShellStdin.
			"matcher": "^(Shell|Write|Delete)$",
			"timeout": 30,
		}
	},
	defaults: map[string]any{"version": 1},
}
