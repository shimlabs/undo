package hook

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/store"
)

// codexMatcher covers shell commands, which Codex reports as Bash, and file
// edits, which it makes with apply_patch.
const codexMatcher = "Bash|apply_patch"

// CodexHooksPath returns the path of the Codex user hooks file.
func CodexHooksPath() (string, error) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "hooks.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

// InstallCodex adds a PreToolUse hook that runs binary before every shell
// command and patch. Codex runs it only after the user trusts it in /hooks.
// It returns the path of the hooks file.
func InstallCodex(binary string) (string, error) {
	path, err := CodexHooksPath()
	if err != nil {
		return "", err
	}
	return path, installEntry(path, "codex", map[string]any{
		"matcher": codexMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellQuote(binary) + " hook codex",
			"timeout": 30,
		}},
	})
}

// UninstallCodex removes the hook added by InstallCodex and leaves everything
// else as it was.
func UninstallCodex() (string, error) {
	path, err := CodexHooksPath()
	if err != nil {
		return "", err
	}
	return path, uninstallEntry(path, "codex")
}

// HandleCodex reads one Codex PreToolUse event from in and takes a checkpoint
// with checkpoint. Errors are logged in the store, and the agent is never
// blocked.
func HandleCodex(in io.Reader, checkpoint store.Checkpointer) error {
	return handlePreToolUse(in, checkpoint, codexLabel)
}

// codexLabel uses the shell command, or for apply_patch, whose command is the
// whole patch, the files the patch touches.
func codexLabel(event preToolUseInput, _ string) string {
	if event.ToolName != "apply_patch" {
		return event.ToolInput.Command
	}
	var files []string
	for line := range strings.Lines(event.ToolInput.Command) {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if file, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				files = append(files, file)
			}
		}
	}
	return strings.TrimSpace("apply_patch " + strings.Join(files, ", "))
}
