package hook

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/store"
)

func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("UNDO_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func checkpoints(t *testing.T, root string) []store.Checkpoint {
	t.Helper()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestHandleLabelsEachAgentsEvents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent Agent
		event string
		label string
	}{
		{"claude shell", claude, `{"cwd":"ROOT","tool_name":"Bash","tool_input":{"command":"rm -rf src"}}`, "rm -rf src"},
		{"claude edit", claude, `{"cwd":"ROOT","tool_name":"Edit","tool_input":{"file_path":"ROOT/main.go"}}`, "Edit main.go"},
		{"codex shell", codex, `{"cwd":"ROOT","tool_name":"Bash","tool_input":{"command":"make clean"}}`, "make clean"},
		{"codex patch", codex, `{"cwd":"ROOT","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Update File: src/main.go\n*** Add File: notes.md\n*** Delete File: old.txt\n*** End Patch\n"}}`, "apply_patch src/main.go, notes.md, old.txt"},
		{"cursor shell", cursor, `{"cwd":"ROOT","tool_name":"Shell","tool_input":{"command":"npm test","cwd":"ROOT"},"workspace_roots":["ROOT"],"cursor_version":"3.17.8"}`, "npm test"},
		{"cursor write without cwd", cursor, `{"tool_name":"Write","tool_input":{"file_path":"ROOT/main.go"},"workspace_roots":["ROOT"],"cursor_version":"3.17.8"}`, "Write main.go"},
		{"cursor delete", cursor, `{"tool_name":"Delete","tool_input":{"file_path":"ROOT/old.go"},"workspace_roots":["ROOT"]}`, "Delete old.go"},
		{"gemini shell", gemini, `{"cwd":"ROOT","tool_name":"run_shell_command","tool_input":{"command":"go generate"}}`, "go generate"},
		{"gemini replace", gemini, `{"cwd":"ROOT","tool_name":"replace","tool_input":{"file_path":"ROOT/a.go","old_string":"x","new_string":"y"}}`, "replace a.go"},
		{"opencode write", openCode, `{"cwd":"ROOT","tool_name":"write","tool_input":{"file_path":"ROOT/a.go"}}`, "write a.go"},
		{"opencode patch", openCode, `{"cwd":"ROOT","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Update File: a.go\n*** End Patch"}}`, "apply_patch a.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentHome(t)
			root := newProject(t)
			t.Chdir(t.TempDir())

			event := strings.ReplaceAll(tc.event, "ROOT", root)
			if err := tc.agent.Handle(strings.NewReader(event), (*store.Store).Take); err != nil {
				t.Fatal(err)
			}

			list := checkpoints(t, root)
			if len(list) != 1 || list[0].Label != tc.label {
				t.Fatalf("checkpoints = %+v, want one labeled %q", list, tc.label)
			}
		})
	}
}

func TestHandleFindsProjectFromEnvironment(t *testing.T) {
	agentHome(t)
	root := newProject(t)
	t.Chdir(t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", root)

	event := `{"tool_name":"Write","tool_input":{"file_path":"` + root + `/main.go"}}`
	if err := claude.Handle(strings.NewReader(event), (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if list := checkpoints(t, root); len(list) != 1 {
		t.Fatalf("checkpoints = %+v, want one in the project from CLAUDE_PROJECT_DIR", list)
	}
}

func TestClaudeHookStepsAsideForCursor(t *testing.T) {
	agentHome(t)
	root := newProject(t)
	event := `{"cwd":"` + root + `","tool_name":"Shell","tool_input":{"command":"ls"},"cursor_version":"3.17.8"}`

	if err := claude.Handle(strings.NewReader(event), (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if list := checkpoints(t, root); len(list) != 1 {
		t.Fatalf("checkpoints = %d, want 1 while the Cursor hook is not installed", len(list))
	}

	if _, err := cursor.Install("/usr/local/bin/undo"); err != nil {
		t.Fatal(err)
	}
	if err := claude.Handle(strings.NewReader(event), (*store.Store).Take); err != nil {
		t.Fatal(err)
	}
	if list := checkpoints(t, root); len(list) != 1 {
		t.Fatalf("checkpoints = %d, want the Claude hook to leave Cursor events to the Cursor hook", len(list))
	}
}
