package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/atomicfile"
)

// installer adds undo's hook for an agent to the agent's configuration and
// removes it again, touching nothing else.
type installer interface {
	install(path, binary, agent string) error
	uninstall(path, agent string) error
	isInstalled(path, agent string) bool
}

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

func (h hookGroups) isInstalled(path, agent string) bool {
	settings, err := readSettings(path)
	return err == nil && slices.ContainsFunc(hookEntries(settings, h.event), func(entry any) bool {
		return slices.ContainsFunc(handlers(entry), isUndoHandler(agent))
	})
}

// isUndoGroup reports whether entry is a group holding only undo's handler.
func isUndoGroup(entry any, agent string) bool {
	list := handlers(entry)
	return len(list) > 0 && !slices.ContainsFunc(list, func(handler any) bool { return !isUndoHandler(agent)(handler) })
}

// removeHandlers removes undo's handlers from every group and drops groups
// that are left empty.
func removeHandlers(groups []any, agent string) []any {
	var kept []any
	for _, entry := range groups {
		group, ok := entry.(map[string]any)
		list := handlers(entry)
		if !ok || list == nil {
			kept = append(kept, entry)
			continue
		}
		remaining := slices.DeleteFunc(slices.Clone(list), isUndoHandler(agent))
		if len(remaining) == 0 {
			continue
		}
		if len(remaining) < len(list) {
			group = maps.Clone(group)
			group["hooks"] = remaining
		}
		kept = append(kept, group)
	}
	return kept
}

func handlers(entry any) []any {
	group, _ := entry.(map[string]any)
	list, _ := group["hooks"].([]any)
	return list
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

func (h hookList) isInstalled(path, agent string) bool {
	settings, err := readSettings(path)
	return err == nil && slices.ContainsFunc(hookEntries(settings, h.event), isUndoHandler(agent))
}

func withCommand(handler map[string]any, command string) map[string]any {
	handler = maps.Clone(handler)
	handler["command"] = command
	return handler
}

// hookCommand returns the shell command that runs binary's hook for agent.
// The path is always quoted, since agents run hooks through a shell.
func hookCommand(binary, agent string) string {
	return "'" + strings.ReplaceAll(binary, "'", `'\''`) + "' hook " + agent
}

// isUndoHandler matches a handler that runs undo's hook for agent: one shell
// word, then exactly "hook <agent>". Matching the whole command keeps undo
// away from the user's own hooks, even ones that mention undo.
func isUndoHandler(agent string) func(handler any) bool {
	arguments := " hook " + agent
	return func(handler any) bool {
		fields, _ := handler.(map[string]any)
		existing, _ := fields["command"].(string)
		program, ok := strings.CutSuffix(existing, arguments)
		if !ok {
			return false
		}
		if len(program) >= 2 && strings.HasPrefix(program, "'") && strings.HasSuffix(program, "'") {
			program = strings.ReplaceAll(program[1:len(program)-1], `'\''`, "'")
		} else if strings.ContainsAny(program, " \t") {
			return false
		}
		return filepath.Base(program) == "undo"
	}
}

// pluginFile installs a plugin file that the agent loads on start. OpenCode
// uses this.
type pluginFile struct {
	content func(binary string) []byte
}

var errForeignPlugin = errors.New("not written by undo, left unchanged")

// install writes the plugin, which starts binary without a shell.
func (p pluginFile) install(path, binary, _ string) error {
	if err := checkPluginOwner(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, p.content(binary), 0o644)
}

func (p pluginFile) uninstall(path, _ string) error {
	if err := checkPluginOwner(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (p pluginFile) isInstalled(path, _ string) bool {
	data, err := os.ReadFile(path)
	return err == nil && isUndoPlugin(data)
}

// checkPluginOwner refuses to change a plugin file that undo did not write.
func checkPluginOwner(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isUndoPlugin(data) {
		return &fs.PathError{Op: "write", Path: path, Err: errForeignPlugin}
	}
	return nil
}

func isUndoPlugin(data []byte) bool {
	return strings.HasPrefix(string(data), pluginHeader)
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
		return nil, fmt.Errorf("%s is not valid JSON, left unchanged: %w", path, err)
	}
	return settings, nil
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
