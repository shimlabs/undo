package hook

import (
	"maps"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/shell"
)

// installer adds undo's hook for an agent to the agent's configuration and
// removes it again, touching nothing else.
type installer interface {
	install(path, binary, agent string) error
	uninstall(path, agent string) error
	isInstalled(path, agent string) bool
}

func withCommand(handler map[string]any, command string) map[string]any {
	handler = maps.Clone(handler)
	handler["command"] = command
	return handler
}

// hookCommand returns the shell command that runs binary's hook for agent.
// The path is always quoted, since agents run hooks through a shell.
func hookCommand(binary, agent string) string {
	return shell.Quote(binary) + " hook " + agent
}

// isUndoHandler matches a handler that runs undo's hook for agent: one shell
// word, then exactly "hook <agent>". Matching the whole command keeps undo
// away from the user's own hooks, even ones that mention undo.
func isUndoHandler(agent string) func(handler any) bool {
	arguments := " hook " + agent
	return func(handler any) bool {
		fields, _ := handler.(map[string]any)
		existing, _ := fields["command"].(string)
		program, ok := strings.CutSuffix(existing, arguments)
		if !ok {
			return false
		}
		if unquoted, isQuoted := shell.Unquote(program); isQuoted {
			program = unquoted
		} else if strings.ContainsAny(program, " \t") {
			return false
		}
		return filepath.Base(program) == "undo"
	}
}
