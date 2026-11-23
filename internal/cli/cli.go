// Package cli is the undo command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// Version is set when the binary is built.
var Version = "dev"

const usage = `undo puts your project back the way it was before an agent's command.

Usage:
  undo                     restore the state before the last command
  undo list                show checkpoints
  undo diff <id>           show what restoring checkpoint <id> would change
  undo to <id>             restore the state at checkpoint <id>
  undo run -- <command>    take a checkpoint, then run <command>
  undo checkpoint          take a checkpoint now
  undo status              show what undo protects
  undo install <agent>     checkpoint before every change the agent makes:
                           claude, codex, cursor, gemini or opencode
  undo uninstall <agent>   remove that hook
  undo version             print the version

Flags:
  --yes                    restore without asking
  --label <text>           label for undo checkpoint
  --copy                   keep full copies, for filesystems without clones
`

// Env holds the directory and streams a command works with.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// StartWatcher starts a background watcher for the project at root. When
	// it is nil, checkpoints scan the project instead.
	StartWatcher func(root string) error
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run runs the command in args and returns the exit code for the process.
func Run(args []string, env Env) int {
	err := dispatch(args, env)
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	if usageErr, ok := errors.AsType[usageError](err); ok {
		fmt.Fprintf(env.Stderr, "undo: %s\n\n%s", usageErr.msg, usage)
		return 2
	}
	switch {
	case errors.Is(err, errDeclined):
		return 1
	case errors.Is(err, store.ErrCloneUnsupported):
		fmt.Fprintf(env.Stderr, "undo: %s, so it cannot protect this project cheaply.\n"+
			"Run 'undo checkpoint --copy' once to keep full copies instead. They take as much disk as the project.\n", err)
		return 1
	default:
		fmt.Fprintf(env.Stderr, "undo: %s\n", err)
		return 1
	}
}

var errDeclined = errors.New("declined")

func dispatch(args []string, env Env) error {
	command, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, rest = args[0], args[1:]
	}
	switch command {
	case "":
		return undoCommand(rest, env)
	case "list":
		return listCommand(env)
	case "diff":
		return diffCommand(rest, env)
	case "to":
		return toCommand(rest, env)
	case "run":
		return runCommand(rest, env)
	case "checkpoint":
		return checkpointCommand(rest, env)
	case "status":
		return statusCommand(env)
	case "install":
		return installCommand(rest, env)
	case "uninstall":
		return uninstallCommand(rest, env)
	case "hook":
		checkpoint := func(s *store.Store, label, dir string) (store.Checkpoint, error) {
			return takeCheckpoint(s, label, dir, env)
		}
		// A hook must never fail, because some agents block the tool when it
		// does. An unknown agent name is handled like any other agent.
		var agent hook.Agent
		if len(rest) > 0 {
			agent, _ = hook.FindAgent(rest[0])
		}
		// Handle logs its failures in the store.
		_ = agent.Handle(env.Stdin, checkpoint)
		return nil
	case "watch":
		return watchCommand(env)
	case "version":
		fmt.Fprintln(env.Stdout, "undo", Version)
		return nil
	case "help":
		fmt.Fprint(env.Stdout, usage)
		return nil
	}
	return usageError{fmt.Sprintf("unknown command %q", command)}
}

func openStore(env Env) (*store.Store, error) {
	root, err := project.Root(env.Dir)
	if err != nil {
		return nil, err
	}
	return store.Open(root)
}

// flags holds the parsed --yes and --label flags and the other arguments.
type flags struct {
	yes   bool
	copy  bool
	label string
	args  []string
}

func parseFlags(args []string) (flags, error) {
	var f flags
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--yes" || arg == "-y":
			f.yes = true
		case arg == "--copy":
			f.copy = true
		case arg == "--label":
			if i+1 == len(args) {
				return f, usageError{"--label needs a value"}
			}
			i++
			f.label = args[i]
		case strings.HasPrefix(arg, "--label="):
			f.label = strings.TrimPrefix(arg, "--label=")
		case strings.HasPrefix(arg, "-"):
			return f, usageError{fmt.Sprintf("unknown flag %q", arg)}
		default:
			f.args = append(f.args, arg)
		}
	}
	return f, nil
}
