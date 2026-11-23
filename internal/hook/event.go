package hook

import (
	"encoding/json"
	"io"
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

// eventFields are the hook event fields that agents share. An agent that
// sends more embeds them in a struct of its own.
type eventFields struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Cwd      string `json:"cwd"`
	} `json:"tool_input"`
}

func decodeEvent[T any](in io.Reader) (T, error) {
	var fields T
	err := json.NewDecoder(in).Decode(&fields)
	return fields, err
}

// event fills in what every agent sends the same way.
func (f eventFields) event() hookEvent {
	return hookEvent{
		Dir:      f.Cwd,
		Session:  f.SessionID,
		ToolName: f.ToolName,
		Command:  f.ToolInput.Command,
		FilePath: f.ToolInput.FilePath,
	}
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
