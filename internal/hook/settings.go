package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/atomicfile"
)

// hookGroups installs into a JSON settings file where hooks.<event> lists
// groups, each with a matcher and its own list of handlers. Claude Code,
// Codex and Gemini CLI use this layout.
type hookGroups struct {
	event   string
	matcher string
	// handler is undo's handler without its command.
	handler map[string]any
}

// install puts undo's handler in a group of its own. A group that holds only
// undo's handler is updated in place, since Codex ties its trust in a hook to
// the hook's position. If the handler sits in a group with the user's own
// handlers, it is moved out of it.
func (h hookGroups) install(path, binary, agent string) error {
	return editSettings(path, nil, func(settings map[string]any) {
		groups := hookEntries(settings, h.event)
		group := map[string]any{"matcher": h.matcher, "hooks": []any{withCommand(h.handler, hookCommand(binary, agent))}}
		i := slices.IndexFunc(groups, func(entry any) bool { return isUndoGroup(entry, agent) })
		groups = removeHandlers(groups, agent)
		if i >= 0 && i <= len(groups) {
			groups = slices.Insert(groups, i, any(group))
		} else {
			groups = append(groups, group)
		}
		setHookEntries(settings, h.event, groups)
	})
}

func (h hookGroups) uninstall(path, agent string) error {
	return editSettings(path, nil, func(settings map[string]any) {
		setHookEntries(settings, h.event, removeHandlers(hookEntries(settings, h.event), agent))
	})
}

func (h hookGroups) isInstalled(path, agent string) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hookEntries(settings, h.event), func(entry any) bool {
		return slices.ContainsFunc(groupHandlers(entry), isUndoHandler(agent))
	}), nil
}

// isUndoGroup reports whether entry is a group holding only undo's handler.
func isUndoGroup(entry any, agent string) bool {
	handlers := groupHandlers(entry)
	return len(handlers) > 0 && !slices.ContainsFunc(handlers, func(handler any) bool { return !isUndoHandler(agent)(handler) })
}

// removeHandlers removes undo's handlers from every group and drops groups
// that are left empty.
func removeHandlers(groups []any, agent string) []any {
	var kept []any
	for _, entry := range groups {
		group, ok := entry.(map[string]any)
		handlers := groupHandlers(entry)
		if !ok || handlers == nil {
			kept = append(kept, entry)
			continue
		}
		remaining := slices.DeleteFunc(slices.Clone(handlers), isUndoHandler(agent))
		if len(remaining) == 0 {
			continue
		}
		if len(remaining) < len(handlers) {
			group = maps.Clone(group)
			group["hooks"] = remaining
		}
		kept = append(kept, group)
	}
	return kept
}

func groupHandlers(entry any) []any {
	group, _ := entry.(map[string]any)
	handlers, _ := group["hooks"].([]any)
	return handlers
}

// hookList installs into a JSON settings file where hooks.<event> lists
// handlers directly. Cursor uses this layout.
type hookList struct {
	event string
	// handler is undo's handler without its command.
	handler map[string]any
	// defaults are top-level settings a new file needs.
	defaults map[string]any
}

func (h hookList) install(path, binary, agent string) error {
	return editSettings(path, h.defaults, func(settings map[string]any) {
		entries := hookEntries(settings, h.event)
		handler := withCommand(h.handler, hookCommand(binary, agent))
		if i := slices.IndexFunc(entries, isUndoHandler(agent)); i >= 0 {
			entries = slices.DeleteFunc(entries, isUndoHandler(agent))
			entries = slices.Insert(entries, min(i, len(entries)), any(handler))
		} else {
			entries = append(entries, handler)
		}
		setHookEntries(settings, h.event, entries)
	})
}

func (h hookList) uninstall(path, agent string) error {
	return editSettings(path, nil, func(settings map[string]any) {
		setHookEntries(settings, h.event, slices.DeleteFunc(hookEntries(settings, h.event), isUndoHandler(agent)))
	})
}

func (h hookList) isInstalled(path, agent string) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hookEntries(settings, h.event), isUndoHandler(agent)), nil
}

// editSettings reads the JSON settings file at path, adds defaults the file
// does not set, applies edit, and writes it back. A file that is not valid
// JSON is left unchanged.
func editSettings(path string, defaults map[string]any, edit func(settings map[string]any)) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	for key, value := range defaults {
		if _, ok := settings[key]; !ok {
			settings[key] = value
		}
	}
	edit(settings)
	return writeSettings(path, settings)
}

func hookEntries(settings map[string]any, event string) []any {
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	return entries
}

func setHookEntries(settings map[string]any, event string, entries []any) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	if len(entries) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = entries
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
		return nil, newSettingsSyntaxError(path, data, err)
	}
	return settings, nil
}

// SettingsSyntaxError is an agent's settings file that is not valid JSON,
// which undo leaves as it is. Line and Column, counted from 1, are where the
// parser stopped, and 0 when it did not say.
type SettingsSyntaxError struct {
	Path   string
	Line   int
	Column int
	Err    error
}

func (e *SettingsSyntaxError) Error() string {
	if e.Line == 0 {
		return e.Path + " is not valid JSON"
	}
	return fmt.Sprintf("%s is not valid JSON (line %d, column %d)", e.Path, e.Line, e.Column)
}

func (e *SettingsSyntaxError) Unwrap() error { return e.Err }

func newSettingsSyntaxError(path string, data []byte, err error) *SettingsSyntaxError {
	settingsErr := &SettingsSyntaxError{Path: path, Err: err}
	var offset int64
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		offset = syntaxErr.Offset
	} else if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		offset = typeErr.Offset
	}
	if offset <= 0 || offset > int64(len(data)) {
		return settingsErr
	}
	// The parser reports how many bytes it read, the last of them the one
	// it stopped at.
	before := data[:offset-1]
	settingsErr.Line = bytes.Count(before, []byte("\n")) + 1
	settingsErr.Column = len(before) - bytes.LastIndexByte(before, '\n')
	return settingsErr
}

// writeSettings replaces the settings file. A settings file that is a
// symlink, as dotfile managers make them, is written through, so the link
// stays.
func writeSettings(path string, settings map[string]any) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
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
	return atomicfile.WriteFile(path, append(data, '\n'), mode)
}
