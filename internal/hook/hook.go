// Package hook connects undo to coding agents: it installs a hook into each
// agent's configuration and takes a checkpoint when the agent calls it.
package hook

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// Agent is a coding agent that undo can install a hook into. Most agents keep
// their hooks in a JSON settings file. OpenCode loads a plugin file instead.
type Agent struct {
	// Name is how the agent is named on the command line.
	Name string
	// Title is the agent's product name.
	Title string
	// Note tells the user what happens after the hook is installed.
	Note string

	configPath func() (string, error)
	// event is the settings key the hook goes under, for agents with a
	// settings file.
	event string
	// entry returns the hook entry that runs command.
	entry func(command string) map[string]any
	// defaults are top-level settings a new settings file needs.
	defaults map[string]any
	// plugin returns the plugin file that runs binary, for agents that load
	// plugins.
	plugin func(binary string) []byte
}

// Agents lists every agent undo supports.
var Agents = []Agent{claude, codex, cursor, gemini, openCode}

// FindAgent returns the agent with the given command-line name.
func FindAgent(name string) (Agent, bool) {
	i := slices.IndexFunc(Agents, func(a Agent) bool { return a.Name == name })
	if i < 0 {
		return Agent{}, false
	}
	return Agents[i], true
}

// Install adds a hook that runs binary before the agent changes files. A hook
// undo installed before is replaced, and everything else in the agent's
// configuration is kept. It returns the path of the file it wrote.
func (a Agent) Install(binary string) (string, error) {
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	if a.plugin != nil {
		return path, writePlugin(path, a.plugin(binary))
	}
	return path, installEntry(path, a.event, a.Name, a.entry(shellQuote(binary)+" hook "+a.Name), a.defaults)
}

// Uninstall removes the hook added by Install and leaves everything else as
// it was. It returns the path of the file it changed.
func (a Agent) Uninstall() (string, error) {
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	if a.plugin != nil {
		return path, removePlugin(path, a.Name)
	}
	return path, uninstallEntry(path, a.event, a.Name)
}

// IsInstalled reports whether undo's hook is in the agent's configuration.
func (a Agent) IsInstalled() bool {
	path, err := a.configPath()
	if err != nil {
		return false
	}
	if a.plugin != nil {
		data, err := os.ReadFile(path)
		return err == nil && isUndoPlugin(data, a.Name)
	}
	settings, err := readSettings(path)
	return err == nil && slices.ContainsFunc(hookEntries(settings, a.event), isUndoEntry(a.Name))
}

// preToolUseInput is the part of a hook event that undo reads. Claude Code,
// Codex, Cursor and Gemini CLI send these fields, and the OpenCode plugin
// sends them too.
type preToolUseInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Cwd      string `json:"cwd"`
	} `json:"tool_input"`
	WorkspaceRoots []string `json:"workspace_roots"`
	CursorVersion  string   `json:"cursor_version"`
}

// Handle reads one hook event from in and takes a checkpoint of the project
// the tool works in. Errors are logged in the store when there is one, and the
// agent is never blocked.
func (a Agent) Handle(in io.Reader, checkpoint store.Checkpointer) error {
	var event preToolUseInput
	if err := json.NewDecoder(in).Decode(&event); err != nil {
		return err
	}
	// Cursor also runs Claude Code's hooks. When undo's Cursor hook is
	// installed, it covers the event, and a second checkpoint would only
	// repeat the first.
	if a.Name == claude.Name && event.CursorVersion != "" && cursor.IsInstalled() {
		return nil
	}
	dir, err := event.dir()
	if err != nil {
		return err
	}
	root, err := project.Root(dir)
	if err != nil {
		return err
	}
	s, err := store.Open(root)
	if err != nil {
		return err
	}
	if _, err := checkpoint(s, event.label(root), dir); err != nil {
		s.LogError(err)
		return err
	}
	return nil
}

// dir returns the directory the tool works in. Agents report it in different
// places, and Cursor runs hooks from its own directory, so the process's
// working directory is the last resort.
func (e preToolUseInput) dir() (string, error) {
	candidates := []string{e.Cwd, e.ToolInput.Cwd}
	if len(e.WorkspaceRoots) > 0 {
		candidates = append(candidates, e.WorkspaceRoots[0])
	}
	candidates = append(candidates, os.Getenv("CLAUDE_PROJECT_DIR"), os.Getenv("CURSOR_PROJECT_DIR"), os.Getenv("GEMINI_PROJECT_DIR"))
	for _, dir := range candidates {
		if dir != "" {
			return dir, nil
		}
	}
	return os.Getwd()
}

// label describes the tool call: the shell command, the files a patch
// touches, or the tool and the file it changes.
func (e preToolUseInput) label(root string) string {
	if e.ToolName == "apply_patch" {
		return patchLabel(e.ToolInput.Command)
	}
	if e.ToolInput.Command != "" {
		return e.ToolInput.Command
	}
	file := e.ToolInput.FilePath
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return strings.TrimSpace(e.ToolName + " " + file)
}

func patchLabel(patch string) string {
	var files []string
	for line := range strings.Lines(patch) {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if file, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				files = append(files, file)
			}
		}
	}
	return strings.TrimSpace("apply_patch " + strings.Join(files, ", "))
}

func homePath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

var errForeignPlugin = errors.New("not written by undo, left unchanged")

func writePlugin(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func removePlugin(path, agent string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isUndoPlugin(data, agent) {
		return &fs.PathError{Op: "remove", Path: path, Err: errForeignPlugin}
	}
	return os.Remove(path)
}

func isUndoPlugin(data []byte, agent string) bool {
	return strings.Contains(string(data), `"hook", "`+agent+`"`)
}
