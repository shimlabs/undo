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

// hookCommand handles one event from an agent. It never fails and never
// prints, because some agents block the tool when a hook fails or prints. An
// unknown agent name is read in the most common event format, and Handle logs
// its failures.
func hookCommand(args []string, env Env) {
	defer func() { _ = recover() }()
	var agent hook.Agent
	if len(args) > 0 {
		agent, _ = hook.FindAgent(args[0])
	}
	_ = agent.Handle(env.Stdin, env.StoreHome, env.checkpointer())
}

// installedAgentTitles returns the product names of the agents that have
// undo's hook.
func installedAgentTitles() []string {
	var titles []string
	for _, agent := range hook.Agents() {
		if agent.IsInstalled() {
			titles = append(titles, agent.Title)
		}
	}
	return titles
}
