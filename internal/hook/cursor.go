package hook

import (
	"cmp"
	"io"
	"os"
)

func cursor() Agent {
	return Agent{
		Name:  "cursor",
		Title: "Cursor",
		Note: "Cursor now takes a checkpoint before every shell command, edit and delete.\n" +
			"Restart Cursor agent sessions that are already running in the terminal.",
		parseEvent: parseCursorEvent,
		configPath: func() (string, error) {
			return homePath(".cursor", "hooks.json")
		},
		installer: hookList{
			event: "preToolUse",
			handler: map[string]any{
				// Cursor matches with an unanchored regex, and a bare Write would
				// also match WriteShellStdin.
				"matcher":    "^(Shell|Write|Delete)$",
				timeoutField: hookTimeout.Seconds(),
			},
			defaults: map[string]any{"version": 1},
		},
	}
}

// cursorFields are the fields Cursor sends on top of the shared ones.
type cursorFields struct {
	eventFields
	ConversationID string   `json:"conversation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	CursorVersion  string   `json:"cursor_version"`
}

// parseCursorEvent reads a Cursor event. Cursor runs hooks from its own
// directory, names the session conversation_id and reports the project as a
// workspace root, and only shell commands come with a cwd.
func parseCursorEvent(in io.Reader) (hookEvent, error) {
	fields, err := decodeEvent[cursorFields](in)
	if err != nil {
		return hookEvent{}, err
	}
	return fields.event(), nil
}

func (f cursorFields) event() hookEvent {
	event := f.eventFields.event()
	var workspaceRoot string
	if len(f.WorkspaceRoots) > 0 {
		workspaceRoot = f.WorkspaceRoots[0]
	}
	event.ProjectDir = cmp.Or(workspaceRoot, os.Getenv("CURSOR_PROJECT_DIR"), f.Cwd)
	event.Dir = cmp.Or(f.ToolInput.Cwd, f.Cwd, event.ProjectDir)
	event.Session = f.ConversationID
	event.IsFromCursor = true
	return event
}
