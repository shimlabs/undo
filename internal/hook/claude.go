package hook

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

const claudeMatcher = "Bash|Edit|Write|MultiEdit|NotebookEdit"

// ClaudeSettingsPath returns the path of the Claude Code user settings file.
func ClaudeSettingsPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// InstallClaude adds a PreToolUse hook that runs binary before every tool that
// can change files. If undo installed a hook before, that hook is replaced.
// All other settings are kept. It returns the path of the settings file.
func InstallClaude(binary string) (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	return path, installEntry(path, "claude", map[string]any{
		"matcher": claudeMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellQuote(binary) + " hook claude",
		}},
	})
}

// UninstallClaude removes the hook added by InstallClaude and leaves everything
// else as it was.
func UninstallClaude() (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	return path, uninstallEntry(path, "claude")
}

// HandleClaude reads one Claude Code PreToolUse event from in and takes a
// checkpoint with checkpoint. Errors are logged in the store, and the agent is
// never blocked.
func HandleClaude(in io.Reader, checkpoint Checkpointer) error {
	return handle(in, checkpoint, claudeLabel)
}

func claudeLabel(event input, root string) string {
	if event.ToolInput.Command != "" {
		return event.ToolInput.Command
	}
	file := event.ToolInput.FilePath
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return strings.TrimSpace(event.ToolName + " " + file)
}
