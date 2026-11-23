// Package hook connects undo to coding agents: it installs a hook into each
// agent's configuration and takes a checkpoint when the agent calls it.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// Agent is a coding agent that undo can install a hook into.
type Agent struct {
	// Name is how the agent is named on the command line.
	Name string
	// Title is the agent's product name.
	Title string
	// Note tells the user what happens after the hook is installed.
	Note string

	configPath func() (string, error)
	installer  installer
	// isHandledElsewhere reports whether another agent's hook already takes
	// a checkpoint for event.
	isHandledElsewhere func(event preToolUseInput) bool
}

// Agents returns every agent undo supports.
func Agents() []Agent {
	return []Agent{claude, codex, cursor, gemini, openCode}
}

// FindAgent returns the agent with the given command-line name.
func FindAgent(name string) (Agent, bool) {
	agents := Agents()
	i := slices.IndexFunc(agents, func(a Agent) bool { return a.Name == name })
	if i < 0 {
		return Agent{}, false
	}
	return agents[i], true
}

// ErrUnsafeBinary means the undo binary lives where it will disappear or
// where other users can replace it, so a hook must not run it.
var ErrUnsafeBinary = errors.New("this undo binary is in a temporary or shared directory; install undo with go install or a package manager first")

// Install adds a hook that runs binary before the agent changes files. A hook
// undo installed before is replaced, and everything else in the agent's
// configuration is kept. It returns the path of the file it wrote.
func (a Agent) Install(binary string) (string, error) {
	if err := checkBinary(binary); err != nil {
		return "", err
	}
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	return path, a.installer.install(path, binary, a.Name)
}

// Uninstall removes the hook added by Install and leaves everything else as
// it was. It returns the path of the file it changed.
func (a Agent) Uninstall() (string, error) {
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	return path, a.installer.uninstall(path, a.Name)
}

// IsInstalled reports whether undo's hook is in the agent's configuration.
func (a Agent) IsInstalled() bool {
	path, err := a.configPath()
	return err == nil && a.installer.isInstalled(path, a.Name)
}

// checkBinary refuses a binary in the temporary directory, where go run
// builds and which is cleaned up later, or in a directory that other users
// can write to, where anyone could put a program in its place.
func checkBinary(binary string) error {
	temps := []string{os.TempDir()}
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		temps = append(temps, resolved)
	}
	for _, temp := range temps {
		if strings.HasPrefix(binary, filepath.Clean(temp)+string(filepath.Separator)) {
			return fmt.Errorf("%s: %w", binary, ErrUnsafeBinary)
		}
	}
	info, err := os.Stat(filepath.Dir(binary))
	if err != nil {
		return nil
	}
	if info.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("%s: %w", binary, ErrUnsafeBinary)
	}
	return nil
}

// preToolUseInput is the part of a hook event that undo reads. Claude Code,
// Codex, Cursor and Gemini CLI send these fields, and the OpenCode plugin
// sends them too. Cursor names its session conversation_id.
type preToolUseInput struct {
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	ToolName       string `json:"tool_name"`
	ToolInput      struct {
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
	if a.isHandledElsewhere != nil && a.isHandledElsewhere(event) {
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
	projectStore, err := store.Open(root)
	if err != nil {
		return err
	}
	options := store.CheckpointOptions{Label: event.label(root), Dir: dir, Agent: a.Name, Session: event.session()}
	if _, err := checkpoint(projectStore, options); err != nil {
		projectStore.LogError(err)
		return err
	}
	return nil
}

func (e preToolUseInput) session() string {
	if e.SessionID != "" {
		return e.SessionID
	}
	return e.ConversationID
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
