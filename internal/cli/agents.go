package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
)

func installCommand(parsed flags, env Env) error {
	agent, err := agentArg("install", parsed.args)
	if err != nil {
		return err
	}
	wasInstalled := agent.IsInstalled()
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
	agent, err := agentArg("uninstall", parsed.args)
	if err != nil {
		return err
	}
	out := env.stdout()
	if !agent.IsInstalled() {
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
