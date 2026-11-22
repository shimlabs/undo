package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// installEntry puts entry under hooks.PreToolUse in the JSON file at path. An
// entry undo added before for agent is replaced in place, since Codex ties its
// trust in a hook to the hook's position. Everything else is kept.
func installEntry(path, agent string, entry map[string]any) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	entries := preToolUse(settings)
	if i := slices.IndexFunc(entries, isUndoEntry(agent)); i >= 0 {
		entries[i] = entry
		entries = slices.Concat(entries[:i+1], slices.DeleteFunc(entries[i+1:], isUndoEntry(agent)))
	} else {
		entries = append(entries, entry)
	}
	setPreToolUse(settings, entries)
	return writeSettings(path, settings)
}

func uninstallEntry(path, agent string) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	setPreToolUse(settings, slices.DeleteFunc(preToolUse(settings), isUndoEntry(agent)))
	return writeSettings(path, settings)
}

func isUndoEntry(agent string) func(entry any) bool {
	return func(entry any) bool {
		data, err := json.Marshal(entry)
		return err == nil && strings.Contains(string(data), " hook "+agent)
	}
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
