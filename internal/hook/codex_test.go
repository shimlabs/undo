package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/store"
)

func codexHome(t *testing.T, hooks string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if hooks != "" {
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(hooks), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "hooks.json")
}

const existingCodex = `{
  "description": "team hooks",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "audit-shell"}]}
    ]
  }
}`

func TestInstallCodexAppendsAfterExistingHooks(t *testing.T) {
	path := codexHome(t, existingCodex)

	if _, err := InstallCodex("/usr/local/bin/undo"); err != nil {
		t.Fatal(err)
	}

	hooks := readSettingsFile(t, path)
	if hooks["description"] != "team hooks" {
		t.Fatalf("description lost: %v", hooks)
	}
	entries := preToolUse(hooks)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse = %v", entries)
	}
	data, _ := json.Marshal(entries[1])
	for _, want := range []string{`"matcher":"Bash|apply_patch"`, `"command":"/usr/local/bin/undo hook codex"`, `"timeout":30`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("undo entry %s is missing %s", data, want)
		}
	}
}

func TestInstallCodexKeepsPosition(t *testing.T) {
	path := codexHome(t, existingCodex)
	InstallCodex("/usr/local/bin/undo")
	hooks := readSettingsFile(t, path)
	setPreToolUse(hooks, append(preToolUse(hooks), map[string]any{"matcher": "Read"}))
	writeSettings(path, hooks)

	InstallCodex("/opt/bin/undo")

	entries := preToolUse(readSettingsFile(t, path))
	data, _ := json.Marshal(entries[1])
	if len(entries) != 3 || !strings.Contains(string(data), "/opt/bin/undo hook codex") {
		t.Fatalf("PreToolUse = %v, want the undo entry updated in place", entries)
	}
}

func TestUninstallCodexRemovesOnlyOurs(t *testing.T) {
	path := codexHome(t, existingCodex)
	InstallCodex("/usr/local/bin/undo")

	if _, err := UninstallCodex(); err != nil {
		t.Fatal(err)
	}

	entries := preToolUse(readSettingsFile(t, path))
	data, _ := json.Marshal(entries)
	if len(entries) != 1 || strings.Contains(string(data), "hook codex") {
		t.Fatalf("PreToolUse = %s", data)
	}
}

func TestCodexHookLabels(t *testing.T) {
	for _, tc := range []struct{ name, toolName, command, want string }{
		{"shell", "Bash", "rm -rf build", "rm -rf build"},
		{"patch", "apply_patch", "*** Begin Patch\n*** Update File: src/main.go\n@@\n-a\n+b\n*** Add File: notes.md\n+hi\n*** Delete File: old.txt\n*** End Patch\n", "apply_patch src/main.go, notes.md, old.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNDO_HOME", t.TempDir())
			root, _ := filepath.EvalSymlinks(t.TempDir())
			input, _ := json.Marshal(map[string]any{
				"cwd":             root,
				"hook_event_name": "PreToolUse",
				"tool_name":       tc.toolName,
				"tool_input":      map[string]string{"command": tc.command},
			})

			if err := HandleCodex(strings.NewReader(string(input)), (*store.Store).Take); err != nil {
				t.Fatal(err)
			}

			s, _ := store.Open(root)
			list, _ := s.List()
			if len(list) != 1 || list[0].Label != tc.want {
				t.Fatalf("checkpoints = %+v, want label %q", list, tc.want)
			}
		})
	}
}
