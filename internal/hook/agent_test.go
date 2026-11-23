package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentHome points every agent's configuration at a fresh home directory and
// returns it.
func agentHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func readJSONFile(t *testing.T, path string) map[string]any {
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

func writeJSONFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// settingsCases are the agents configured through a JSON settings file, each
// with a file that already holds a setting and a hook of the user's own.
var settingsCases = []struct {
	agent    Agent
	file     string
	existing string
	keep     string
}{
	{claude, ".claude/settings.json", `{"model": "opus", "hooks": {"PreToolUse": [{"matcher": "Read", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "model"},
	{codex, ".codex/hooks.json", `{"description": "team hooks", "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "description"},
	{cursor, ".cursor/hooks.json", `{"version": 1, "hooks": {"preToolUse": [{"command": "audit", "matcher": "Read"}], "afterFileEdit": [{"command": "format"}]}}`, "version"},
	{gemini, ".gemini/settings.json", `{"theme": "dark", "hooks": {"BeforeTool": [{"matcher": "read_file", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "theme"},
}

func undoEntries(t *testing.T, agent Agent, settings map[string]any) (all []any, ours []int) {
	t.Helper()
	all = hookEntries(settings, agent.event)
	for i, entry := range all {
		data, _ := json.Marshal(entry)
		if strings.Contains(string(data), " hook "+agent.Name) {
			ours = append(ours, i)
		}
	}
	return all, ours
}

func TestInstallKeepsOtherSettings(t *testing.T) {
	for _, tc := range settingsCases {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := agentHome(t)
			path := filepath.Join(home, tc.file)
			writeJSONFile(t, path, tc.existing)

			got, err := tc.agent.Install("/usr/local/bin/undo")
			if err != nil {
				t.Fatal(err)
			}
			if got != path {
				t.Fatalf("Install wrote %s, want %s", got, path)
			}
			settings := readJSONFile(t, path)
			if _, ok := settings[tc.keep]; !ok {
				t.Fatalf("%q setting lost: %v", tc.keep, settings)
			}
			all, ours := undoEntries(t, tc.agent, settings)
			if len(all) != 2 || len(ours) != 1 || ours[0] != 1 {
				t.Fatalf("hook entries = %v", all)
			}
			if !tc.agent.IsInstalled() {
				t.Fatal("IsInstalled = false after Install")
			}
		})
	}
}

func TestReinstallUpdatesInPlace(t *testing.T) {
	for _, tc := range settingsCases {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := agentHome(t)
			path := filepath.Join(home, tc.file)
			writeJSONFile(t, path, tc.existing)
			tc.agent.Install("/usr/local/bin/undo")
			settings := readJSONFile(t, path)
			setHookEntries(settings, tc.agent.event, append(hookEntries(settings, tc.agent.event), map[string]any{"matcher": "later"}))
			writeSettings(path, settings)

			tc.agent.Install("/opt/bin/undo")

			all, ours := undoEntries(t, tc.agent, readJSONFile(t, path))
			data, _ := json.Marshal(all)
			if len(all) != 3 || len(ours) != 1 || ours[0] != 1 || !strings.Contains(string(data), "/opt/bin/undo hook "+tc.agent.Name) {
				t.Fatalf("hook entries = %s, want the undo entry updated in place", data)
			}
		})
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	for _, tc := range settingsCases {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := agentHome(t)
			path := filepath.Join(home, tc.file)
			writeJSONFile(t, path, tc.existing)
			tc.agent.Install("/usr/local/bin/undo")

			if _, err := tc.agent.Uninstall(); err != nil {
				t.Fatal(err)
			}

			settings := readJSONFile(t, path)
			all, ours := undoEntries(t, tc.agent, settings)
			if len(all) != 1 || len(ours) != 0 {
				t.Fatalf("hook entries = %v", all)
			}
			if _, ok := settings[tc.keep]; !ok {
				t.Fatalf("%q setting lost: %v", tc.keep, settings)
			}
			if tc.agent.IsInstalled() {
				t.Fatal("IsInstalled = true after Uninstall")
			}
		})
	}
}

func TestInstallCursorCreatesVersion(t *testing.T) {
	home := agentHome(t)

	if _, err := cursor.Install("/usr/local/bin/undo"); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(home, ".cursor", "hooks.json"))
	if settings["version"] != float64(1) {
		t.Fatalf("version = %v, want 1", settings["version"])
	}
}

func TestInstallRefusesInvalidJSON(t *testing.T) {
	home := agentHome(t)
	path := filepath.Join(home, ".cursor", "hooks.json")
	writeJSONFile(t, path, "{\n  // a comment\n  \"version\": 1\n}")

	if _, err := cursor.Install("/usr/local/bin/undo"); err == nil {
		t.Fatal("Install rewrote a file it could not parse")
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "// a comment") {
		t.Fatalf("file changed: %s", data)
	}
}

func TestInstallOpenCodeWritesPlugin(t *testing.T) {
	home := agentHome(t)
	path := filepath.Join(home, ".config", "opencode", "plugins", "undo.js")

	got, err := openCode.Install("/opt/my tools/undo")
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("Install wrote %s, want %s", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"/opt/my tools/undo"`) {
		t.Fatalf("plugin does not run the given binary:\n%s", data)
	}
	if !openCode.IsInstalled() {
		t.Fatal("IsInstalled = false after Install")
	}

	if _, err := openCode.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plugin still there after Uninstall: %v", err)
	}
}

func TestUninstallOpenCodeKeepsForeignPlugin(t *testing.T) {
	home := agentHome(t)
	path := filepath.Join(home, ".config", "opencode", "plugins", "undo.js")
	writeJSONFile(t, path, "export const Mine = async () => ({})\n")

	if _, err := openCode.Uninstall(); err == nil {
		t.Fatal("Uninstall removed a plugin undo did not write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestFindAgent(t *testing.T) {
	for _, name := range []string{"claude", "codex", "cursor", "gemini", "opencode"} {
		if agent, ok := FindAgent(name); !ok || agent.Name != name {
			t.Errorf("FindAgent(%q) = %v, %v", name, agent.Name, ok)
		}
	}
	if _, ok := FindAgent("vim"); ok {
		t.Error("FindAgent found an agent that does not exist")
	}
}
