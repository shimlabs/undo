package cli

import (
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
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
	out := env.stdout()
	out.Success("Installed the undo hook for %s", agent.Title)
	out.Printf("  %s %s\n\n%s\n", out.Dim("in"), displayPath(path), agent.Note)
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
	env.stdout().Success("Removed the undo hook for %s from %s", agent.Title, displayPath(path))
	return nil
}

func agentArg(command string, args []string) (hook.Agent, error) {
	agents := strings.Join(hook.AgentNames(), ", ")
	if len(args) == 0 {
		return hook.Agent{}, usageError{message: fmt.Sprintf("undo %s needs an agent: %s", command, agents)}
	}
	if agent, ok := hook.FindAgent(args[0]); ok {
		return agent, nil
	}
	return hook.Agent{}, usageError{message: fmt.Sprintf("%q is not an agent undo supports: %s", args[0], agents)}
}

// hookCommand handles one event from an agent. It never fails and never
// prints, because some agents block the tool when a hook fails or prints.
// Failures go to the error log instead.
func hookCommand(args []string, env Env) {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			store.LogHomeError(env.StoreHome, fmt.Errorf("%s hook panicked: %v", name, recovered))
		}
	}()
	agent, ok := hook.FindAgent(name)
	if !ok {
		store.LogHomeError(env.StoreHome, fmt.Errorf("hook for unknown agent %q", name))
		return
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
