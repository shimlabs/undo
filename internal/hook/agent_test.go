package hook

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

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

// settingsCase is an agent configured through a JSON settings file, with a
// file that already holds a setting and a hook of the user's own.
type settingsCase struct {
	agent    Agent
	file     string
	existing string
	keep     string
}

func settingsCases() []settingsCase {
	return []settingsCase{
		{claude(), ".claude/settings.json", `{"model": "opus", "hooks": {"PreToolUse": [{"matcher": "Read", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "model"},
		{codex(), ".codex/hooks.json", `{"description": "team hooks", "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "description"},
		{cursor(), ".cursor/hooks.json", `{"version": 1, "hooks": {"preToolUse": [{"command": "audit", "matcher": "Read"}], "afterFileEdit": [{"command": "format"}]}}`, "version"},
		{gemini(), ".gemini/settings.json", `{"theme": "dark", "hooks": {"BeforeTool": [{"matcher": "read_file", "hooks": [{"type": "command", "command": "audit"}]}]}}`, "theme"},
	}
}

// settingsEvent returns the settings key an agent's hooks go under.
func settingsEvent(agent Agent) string {
	switch installer := agent.installer.(type) {
	case hookGroups:
		return installer.event
	case hookList:
		return installer.event
	}
	return ""
}

func undoEntries(t *testing.T, agent Agent, settings map[string]any) (all []any, ours []int) {
	t.Helper()
	all = hookEntries(settings, settingsEvent(agent))
	for i, entry := range all {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), " hook "+agent.Name) {
			ours = append(ours, i)
		}
	}
	return all, ours
}

func TestInstallKeepsOtherSettings(t *testing.T) {
	for _, tc := range settingsCases() {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := testutil.AgentHome(t)
			path := filepath.Join(home, tc.file)
			testutil.WriteFile(t, path, tc.existing)

			got, err := tc.agent.Install(testutil.PrivateBinary(t, "bin"))
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
	for _, tc := range settingsCases() {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := testutil.AgentHome(t)
			path := filepath.Join(home, tc.file)
			testutil.WriteFile(t, path, tc.existing)
			if _, err := tc.agent.Install(testutil.PrivateBinary(t, "bin")); err != nil {
				t.Fatal(err)
			}
			settings := readJSONFile(t, path)
			setHookEntries(settings, settingsEvent(tc.agent), append(hookEntries(settings, settingsEvent(tc.agent)), map[string]any{"matcher": "later"}))
			if err := writeSettings(path, settings); err != nil {
				t.Fatal(err)
			}

			updated := testutil.PrivateBinary(t, "new-bin")
			if _, err := tc.agent.Install(updated); err != nil {
				t.Fatal(err)
			}

			all, ours := undoEntries(t, tc.agent, readJSONFile(t, path))
			data, err := json.Marshal(all)
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 3 || len(ours) != 1 || ours[0] != 1 || !strings.Contains(string(data), "'"+updated+"' hook "+tc.agent.Name) {
				t.Fatalf("hook entries = %s, want the undo entry updated in place", data)
			}
		})
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	for _, tc := range settingsCases() {
		t.Run(tc.agent.Name, func(t *testing.T) {
			home := testutil.AgentHome(t)
			path := filepath.Join(home, tc.file)
			testutil.WriteFile(t, path, tc.existing)
			if _, err := tc.agent.Install(testutil.PrivateBinary(t, "bin")); err != nil {
				t.Fatal(err)
			}

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
	home := testutil.AgentHome(t)

	if _, err := cursor().Install(testutil.PrivateBinary(t, "bin")); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(home, ".cursor", "hooks.json"))
	if settings["version"] != float64(1) {
		t.Fatalf("version = %v, want 1", settings["version"])
	}
}

func TestInstallRefusesInvalidJSON(t *testing.T) {
	home := testutil.AgentHome(t)
	path := filepath.Join(home, ".cursor", "hooks.json")
	testutil.WriteFile(t, path, "{\n  // a comment\n  \"version\": 1\n}")

	if _, err := cursor().Install(testutil.PrivateBinary(t, "bin")); err == nil {
		t.Fatal("Install rewrote a file it could not parse")
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "// a comment") {
		t.Fatalf("file changed: %s", data)
	}
}

func TestInstallOpenCodeWritesPlugin(t *testing.T) {
	home := testutil.AgentHome(t)
	path := filepath.Join(home, ".config", "opencode", "plugins", "undo.js")

	binary := testutil.PrivateBinary(t, "my tools")
	got, err := openCode().Install(binary)
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
	if !strings.Contains(string(data), strconv.Quote(binary)) {
		t.Fatalf("plugin does not run the given binary:\n%s", data)
	}
	if !openCode().IsInstalled() {
		t.Fatal("IsInstalled = false after Install")
	}

	if _, err := openCode().Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("plugin still there after Uninstall: %v", err)
	}
}

func TestUninstallOpenCodeKeepsForeignPlugin(t *testing.T) {
	home := testutil.AgentHome(t)
	path := filepath.Join(home, ".config", "opencode", "plugins", "undo.js")
	testutil.WriteFile(t, path, "export const Mine = async () => ({})\n")

	if _, err := openCode().Uninstall(); err == nil {
		t.Fatal("Uninstall removed a plugin undo did not write")
	}
	if _, err := openCode().Install(testutil.PrivateBinary(t, "bin")); err == nil {
		t.Fatal("Install overwrote a plugin undo did not write")
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

func TestInstallKeepsUserHooksInSameGroup(t *testing.T) {
	home := testutil.AgentHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	testutil.WriteFile(t, path, `{"hooks": {"PreToolUse": [
		{"matcher": "Bash", "hooks": [
			{"type": "command", "command": "'/usr/local/bin/undo' hook claude"},
			{"type": "command", "command": "dcg check"}
		]},
		{"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/notify hook claude-code"}]}
	]}}`)

	binary := testutil.PrivateBinary(t, "bin")
	if _, err := claude().Install(binary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dcg check", "notify hook claude-code", "'" + binary + "' hook claude"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("after install, %q is missing:\n%s", want, data)
		}
	}

	if _, err := claude().Uninstall(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "dcg check") || !strings.Contains(string(data), "notify hook claude-code") {
		t.Fatalf("uninstall removed the user's own hooks:\n%s", data)
	}
	if strings.Contains(string(data), "undo' hook claude\"") {
		t.Fatalf("uninstall left undo's hook:\n%s", data)
	}
}

func TestInstallKeepsOtherProgramsNamedLikeHooks(t *testing.T) {
	home := testutil.AgentHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	testutil.WriteFile(t, path, `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh hook claude"}]}]}}`)

	if _, err := claude().Install(testutil.PrivateBinary(t, "bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := claude().Uninstall(); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "guard.sh hook claude") {
		t.Fatalf("a user's own hook was treated as undo's:\n%s", data)
	}
}

func TestInstallQuotesBinaryPath(t *testing.T) {
	home := testutil.AgentHome(t)
	binary := testutil.PrivateBinary(t, "R&D tools")
	if _, err := claude().Install(binary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), strings.ReplaceAll("\"'"+binary+"' hook claude\"", "&", `\u0026`)) {
		t.Fatalf("binary path is not quoted:\n%s", data)
	}
}

func TestInstallRefusesTemporaryBinary(t *testing.T) {
	testutil.AgentHome(t)
	for _, binary := range []string{filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "undo"), "/tmp/undo", "/var/tmp/undo"} {
		if _, err := claude().Install(binary); !errors.Is(err, ErrUnsafeBinary) {
			t.Errorf("Install(%s) = %v, want ErrUnsafeBinary", binary, err)
		}
	}
}

func TestInstallKeepsSymlinkedSettings(t *testing.T) {
	home := testutil.AgentHome(t)
	dotfiles := filepath.Join(t.TempDir(), "settings.json")
	testutil.WriteFile(t, dotfiles, `{"model": "opus"}`)
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, link); err != nil {
		t.Fatal(err)
	}

	if _, err := claude().Install(testutil.PrivateBinary(t, "bin")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("install replaced a symlinked settings file with a regular file")
	}
	if data, _ := os.ReadFile(dotfiles); !strings.Contains(string(data), "hook claude") {
		t.Fatalf("the hook was not written through the symlink:\n%s", data)
	}
}

func TestEveryAgentIsComplete(t *testing.T) {
	for _, agent := range Agents() {
		if agent.Name == "" || agent.Title == "" || agent.parseEvent == nil || agent.configPath == nil || agent.installer == nil {
			t.Errorf("agent %q is missing a name, title, event parser, config path or installer", agent.Name)
		}
	}
}
