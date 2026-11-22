package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

func claudeConfig(t *testing.T, settings string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "settings.json")
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func countUndoEntries(entries []any) int {
	n := 0
	for _, e := range entries {
		data, _ := json.Marshal(e)
		if strings.Contains(string(data), "undo hook claude") {
			n++
		}
	}
	return n
}

const existing = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Read", "hooks": [{"type": "command", "command": "audit-read"}]}
    ]
  }
}`

func TestInstallPreservesSettings(t *testing.T) {
	path := claudeConfig(t, existing)

	if _, err := InstallClaude("/usr/local/bin/undo"); err != nil {
		t.Fatal(err)
	}

	settings := readSettingsFile(t, path)
	if settings["model"] != "opus" {
		t.Fatalf("model setting lost: %v", settings)
	}
	entries := preToolUse(settings)
	if len(entries) != 2 || countUndoEntries(entries) != 1 {
		t.Fatalf("PreToolUse = %v", entries)
	}
}

func TestInstallCreatesSettings(t *testing.T) {
	path := claudeConfig(t, "")

	if _, err := InstallClaude("/usr/local/bin/undo"); err != nil {
		t.Fatal(err)
	}
	if countUndoEntries(preToolUse(readSettingsFile(t, path))) != 1 {
		t.Fatal("hook not installed")
	}
}

func TestInstallIdempotent(t *testing.T) {
	path := claudeConfig(t, existing)

	InstallClaude("/usr/local/bin/undo")
	InstallClaude("/opt/bin/undo")

	entries := preToolUse(readSettingsFile(t, path))
	if countUndoEntries(entries) != 1 {
		t.Fatalf("PreToolUse = %v", entries)
	}
	data, _ := json.Marshal(entries)
	if !strings.Contains(string(data), "/opt/bin/undo hook claude") {
		t.Fatalf("command not updated to the new binary: %s", data)
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	path := claudeConfig(t, existing)
	InstallClaude("/usr/local/bin/undo")

	if _, err := UninstallClaude(); err != nil {
		t.Fatal(err)
	}

	entries := preToolUse(readSettingsFile(t, path))
	if len(entries) != 1 || countUndoEntries(entries) != 0 {
		t.Fatalf("PreToolUse = %v", entries)
	}
}

func TestHookTakesCheckpoint(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	input := `{"session_id":"s1","cwd":"` + root + `","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf src"}}`

	HandleClaude(strings.NewReader(input), (*store.Store).Take)

	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Label != "rm -rf src" {
		t.Fatalf("checkpoints = %+v", list)
	}
	if got := project.ID(root); !strings.HasSuffix(s.Dir(), got) {
		t.Fatalf("store %s not keyed by project id %s", s.Dir(), got)
	}
}

func TestHookLabelsFileTools(t *testing.T) {
	t.Setenv("UNDO_HOME", t.TempDir())
	root, _ := filepath.EvalSymlinks(t.TempDir())
	input := `{"cwd":"` + root + `","tool_name":"Edit","tool_input":{"file_path":"` + root + `/main.go"}}`

	HandleClaude(strings.NewReader(input), (*store.Store).Take)

	s, _ := store.Open(root)
	list, _ := s.List()
	if len(list) != 1 || list[0].Label != "Edit main.go" {
		t.Fatalf("checkpoints = %+v", list)
	}
}
