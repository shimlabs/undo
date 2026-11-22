// Package hook connects undo to coding agents.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
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
	settings, err := readSettings(path)
	if err != nil {
		return "", err
	}
	entries := slices.DeleteFunc(preToolUse(settings), isUndoEntry)
	entries = append(entries, map[string]any{
		"matcher": claudeMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellQuote(binary) + " hook claude",
		}},
	})
	setPreToolUse(settings, entries)
	return path, writeSettings(path, settings)
}

// UninstallClaude removes the hook added by InstallClaude and leaves everything
// else as it was.
func UninstallClaude() (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	settings, err := readSettings(path)
	if err != nil {
		return "", err
	}
	setPreToolUse(settings, slices.DeleteFunc(preToolUse(settings), isUndoEntry))
	return path, writeSettings(path, settings)
}

type claudeInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// Checkpointer takes a checkpoint of the project in s.
type Checkpointer func(s *store.Store, label, dir string) (store.Checkpoint, error)

// HandleClaude reads one PreToolUse event from in and takes a checkpoint with
// checkpoint. Errors are written to the project's hook log, and the agent is
// never blocked.
func HandleClaude(in io.Reader, checkpoint Checkpointer) error {
	var input claudeInput
	if err := json.NewDecoder(in).Decode(&input); err != nil {
		return err
	}
	dir := input.Cwd
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	root, err := project.Root(dir)
	if err != nil {
		return err
	}
	s, err := store.Open(root)
	if err != nil {
		return err
	}
	if _, err := checkpoint(s, claudeLabel(input, root), dir); err != nil {
		logFailure(s, err)
		return err
	}
	return nil
}

func claudeLabel(input claudeInput, root string) string {
	if input.ToolInput.Command != "" {
		return input.ToolInput.Command
	}
	file := input.ToolInput.FilePath
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return strings.TrimSpace(input.ToolName + " " + file)
}

func logFailure(s *store.Store, failure error) {
	file, err := os.OpenFile(filepath.Join(s.Dir(), "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s %v\n", time.Now().Format(time.RFC3339), failure)
}

func isUndoEntry(entry any) bool {
	data, err := json.Marshal(entry)
	return err == nil && strings.Contains(string(data), " hook claude")
}

func preToolUse(settings map[string]any) []any {
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	return entries
}

func setPreToolUse(settings map[string]any, entries []any) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	if len(entries) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = entries
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
}

func readSettings(path string) (map[string]any, error) {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, left unchanged: %w", path, err)
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".undo-tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
