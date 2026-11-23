package cli

import (
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
)

func installCommand(args []string, env Env) error {
	agent, err := agentArg("install", args)
	if err != nil {
		return err
	}
	path, err := agent.Install(env.Executable)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Updated %s\n%s\n", path, agent.Note)
	return nil
}

func uninstallCommand(args []string, env Env) error {
	agent, err := agentArg("uninstall", args)
	if err != nil {
		return err
	}
	path, err := agent.Uninstall()
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Removed the undo hook from %s\n", path)
	return nil
}

func agentArg(command string, args []string) (hook.Agent, error) {
	if len(args) == 1 {
		if agent, ok := hook.FindAgent(args[0]); ok {
			return agent, nil
		}
	}
	var names []string
	for _, agent := range hook.Agents() {
		names = append(names, agent.Name)
	}
	return hook.Agent{}, usageError{fmt.Sprintf("%s supports: %s", command, strings.Join(names, ", "))}
}

// installedAgents returns the names of the agents that have undo's hook.
func installedAgents() []string {
	var titles []string
	for _, agent := range hook.Agents() {
		if agent.IsInstalled() {
			titles = append(titles, agent.Title)
		}
	}
	return titles
}
