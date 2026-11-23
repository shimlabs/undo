package hook

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// hookEvent is what undo needs from a hook event, whichever agent sent it.
type hookEvent struct {
	// ProjectDir is the folder the agent session works in. Its project gets
	// the checkpoint.
	ProjectDir string
	// Dir is the folder the tool runs in, which can be anywhere.
	Dir      string
	Session  string
	ToolName string
	Command  string
	FilePath string
	// IsFromCursor is set when Cursor sent the event, which it also does for
	// Claude Code's hooks.
	IsFromCursor bool
}

// eventFields are the hook event fields the agents send. Each agent's parser
// picks the ones that agent uses.
type eventFields struct {
	Cwd            string `json:"cwd"`
	ProjectDir     string `json:"project_dir"`
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

func decodeFields(in io.Reader) (eventFields, error) {
	var fields eventFields
	err := json.NewDecoder(in).Decode(&fields)
	return fields, err
}

// event fills in what every agent sends the same way.
func (f eventFields) event() hookEvent {
	return hookEvent{
		Dir:          f.Cwd,
		Session:      f.SessionID,
		ToolName:     f.ToolName,
		Command:      f.ToolInput.Command,
		FilePath:     f.ToolInput.FilePath,
		IsFromCursor: f.CursorVersion != "",
	}
}

// parseClaudeEvent reads a Claude Code event. Claude Code passes the folder
// the session started in as CLAUDE_PROJECT_DIR. Cursor runs Claude Code's
// hooks too, with its own fields.
func parseClaudeEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeFields(in)
	if err != nil {
		return hookEvent{}, err
	}
	if fields.CursorVersion != "" {
		return cursorEvent(fields), nil
	}
	event := fields.event()
	event.ProjectDir = firstOf(os.Getenv("CLAUDE_PROJECT_DIR"), fields.Cwd)
	return event, nil
}

// parseCodexEvent reads a Codex event, whose cwd is the session's folder.
func parseCodexEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeFields(in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = fields.Cwd
	return event, nil
}

// parseCursorEvent reads a Cursor event. Cursor runs hooks from its own
// directory, names the session conversation_id and reports the project as a
// workspace root, and only shell commands come with a cwd.
func parseCursorEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeFields(in)
	if err != nil {
		return hookEvent{}, err
	}
	return cursorEvent(fields), nil
}

func cursorEvent(fields eventFields) hookEvent {
	event := fields.event()
	var workspaceRoot string
	if len(fields.WorkspaceRoots) > 0 {
		workspaceRoot = fields.WorkspaceRoots[0]
	}
	event.ProjectDir = firstOf(workspaceRoot, os.Getenv("CURSOR_PROJECT_DIR"), fields.Cwd)
	event.Dir = firstOf(fields.ToolInput.Cwd, fields.Cwd, event.ProjectDir)
	event.Session = fields.ConversationID
	event.IsFromCursor = true
	return event
}

// parseGeminiEvent reads a Gemini CLI event. Gemini CLI passes the project
// folder as GEMINI_PROJECT_DIR.
func parseGeminiEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeFields(in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = firstOf(os.Getenv("GEMINI_PROJECT_DIR"), fields.Cwd)
	return event, nil
}

// parseOpenCodeEvent reads an event from undo's OpenCode plugin, which sends
// the folder OpenCode was started in as project_dir.
func parseOpenCodeEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeFields(in)
	if err != nil {
		return hookEvent{}, err
	}
	event := fields.event()
	event.ProjectDir = firstOf(fields.ProjectDir, fields.Cwd)
	return event, nil
}

func firstOf(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// label describes the tool call: the shell command, the files a patch
// touches, or the tool and the file it changes.
func (e hookEvent) label(root string) string {
	if e.ToolName == "apply_patch" {
		return patchLabel(e.Command)
	}
	if e.Command != "" {
		return e.Command
	}
	file := e.FilePath
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
