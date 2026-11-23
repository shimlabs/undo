package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
)

func installCommand(parsed flags, env Env) error {
	agent, err := parseAgent("install", parsed.args)
	if err != nil {
		return err
	}
	wasInstalled, err := agent.IsInstalled()
	if err != nil {
		return err
	}
	path, err := agent.Install(env.Executable)
	if err != nil {
		return err
	}
	out := env.stdout()
	if wasInstalled {
		out.Successf("The undo hook for %s is already installed in %s.", agent.Title, displayPath(path))
		return nil
	}
	out.Successf("Installed the undo hook for %s in %s.", agent.Title, displayPath(path))
	out.Println()
	out.Println(agent.Note)
	return nil
}

func uninstallCommand(parsed flags, env Env) error {
	agent, err := parseAgent("uninstall", parsed.args)
	if err != nil {
		return err
	}
	out := env.stdout()
	isInstalled, err := agent.IsInstalled()
	if err != nil {
		return err
	}
	if !isInstalled {
		out.Printf("The undo hook for %s is not installed.\n", agent.Title)
		return nil
	}
	path, err := agent.Uninstall()
	if err != nil {
		return err
	}
	out.Successf("Removed the undo hook for %s from %s.", agent.Title, displayPath(path))
	return nil
}

func parseAgent(commandName string, args []string) (hook.Agent, error) {
	agents := strings.Join(hook.AgentNames(), ", ")
	if len(args) == 0 {
		return hook.Agent{}, usageError{message: fmt.Sprintf("undo %s needs an agent: %s", commandName, agents)}
	}
	if agent, ok := hook.FindAgent(args[0]); ok {
		return agent, nil
	}
	return hook.Agent{}, usageError{message: fmt.Sprintf("%q is not an agent undo supports: %s", args[0], agents)}
}

// hookCommand handles one event from an agent. It never fails and never
// prints, because some agents block the tool when a hook fails or prints.
// Failures go to the error log instead.
func hookCommand(parsed flags, env Env) error {
	name := ""
	if len(parsed.args) > 0 {
		name = parsed.args[0]
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			store.LogHomeError(env.StoreHome, fmt.Errorf("%s %w: %v", name, errHookPanicked, recovered))
		}
	}()
	agent, ok := hook.FindAgent(name)
	if !ok {
		store.LogHomeError(env.StoreHome, fmt.Errorf("%w %q", errUnknownAgent, name))
		return nil
	}
	_ = agent.Handle(env.Stdin, env.StoreHome, env.checkpointer())
	return nil
}

var (
	errHookPanicked = errors.New("hook panicked")
	errUnknownAgent = errors.New("hook for unknown agent")
)

// agentHooks returns the product names of the agents that have undo's hook,
// and a description of each agent whose settings could not be read.
func agentHooks() (installed, unreadable []string) {
	for _, agent := range hook.Agents() {
		isInstalled, err := agent.IsInstalled()
		switch {
		case err != nil:
			unreadable = append(unreadable, agent.Title+": "+userMessage(err))
		case isInstalled:
			installed = append(installed, agent.Title)
		}
	}
	return installed, unreadable
}
