package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
)

func installCommand(args []string, env Env) error {
	agent, err := agentArg("install", args)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	path, err := agent.Install(binary)
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
	return hook.Agent{}, usageError{fmt.Sprintf("%s supports: %s", command, strings.Join(agentNames(), ", "))}
}

func agentNames() []string {
	var names []string
	for _, agent := range hook.Agents {
		names = append(names, agent.Name)
	}
	return names
}
