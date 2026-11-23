// Package cli is the undo command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
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
  --project <path>         use the project at <path>, even one that was deleted
`

// Env holds the directory and streams a command works with.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Executable is the path of the undo binary, which installed hooks run.
	Executable string
	// StartWatcher starts a background watcher for the project at root and
	// returns a channel that receives its error if it exits. When it is nil,
	// checkpoints scan the project instead.
	StartWatcher func(root string) (exited <-chan error, err error)
}

// checkpointer takes checkpoints through the project's watcher when one can
// be started, and by scanning the project otherwise.
func (env Env) checkpointer() store.Checkpointer {
	return func(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
		if env.StartWatcher == nil {
			return projectStore.Take(options)
		}
		return watch.Checkpoint(projectStore, options, func() (<-chan error, error) {
			return env.StartWatcher(projectStore.Root())
		})
	}
}

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

var errDeclined = errors.New("declined")

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
		fmt.Fprintf(env.Stderr, "undo: %s\n\n%s", usageErr.message, usage)
		return 2
	}
	switch {
	case errors.Is(err, errDeclined):
	case errors.Is(err, store.ErrCloneUnsupported):
		fmt.Fprintf(env.Stderr, "undo: %s, so it cannot protect this project cheaply.\n"+
			"Run 'undo checkpoint --copy' once to keep full copies instead. They take as much disk as the project.\n", err)
	case errors.Is(err, store.ErrNotProject):
		fmt.Fprintf(env.Stderr, "undo: %s.\nRun undo, or start your agent, inside a project folder.\n", err)
	default:
		fmt.Fprintf(env.Stderr, "undo: %s\n", err)
	}
	return 1
}

func dispatch(args []string, env Env) error {
	command, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, rest = args[0], args[1:]
	}
	switch command {
	case "":
		return undoCommand(rest, env)
	case "list":
		return listCommand(rest, env)
	case "diff":
		return diffCommand(rest, env)
	case "to":
		return toCommand(rest, env)
	case "run":
		return runCommand(rest, env)
	case "checkpoint":
		return checkpointCommand(rest, env)
	case "status":
		return statusCommand(rest, env)
	case "install":
		return installCommand(rest, env)
	case "uninstall":
		return uninstallCommand(rest, env)
	case "hook":
		hookCommand(rest, env)
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

// flags holds the parsed command-line flags and the remaining arguments.
type flags struct {
	isConfirmed bool
	isCopyMode  bool
	label       string
	project     string
	args        []string
}

func parseFlags(args []string) (flags, error) {
	var parsed flags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case arg == "--yes" || arg == "-y":
			parsed.isConfirmed = true
		case arg == "--copy":
			parsed.isCopyMode = true
		case name == "--label" || name == "--project":
			if !hasValue {
				if i+1 == len(args) {
					return parsed, usageError{name + " needs a value"}
				}
				i++
				value = args[i]
			}
			if name == "--label" {
				parsed.label = value
			} else {
				parsed.project = value
			}
		case strings.HasPrefix(arg, "-"):
			return parsed, usageError{fmt.Sprintf("unknown flag %q", arg)}
		default:
			parsed.args = append(parsed.args, arg)
		}
	}
	return parsed, nil
}

// openStore opens the store of the project in env.Dir, or of the project
// that --project names.
func openStore(env Env, parsed flags) (*store.Store, error) {
	if parsed.project == "" {
		root, err := project.Root(env.Dir)
		if err != nil {
			return nil, err
		}
		return store.Open(root)
	}
	root, err := deletedOrExistingRoot(parsed.project, env.Dir)
	if err != nil {
		return nil, err
	}
	return store.Open(root)
}

// deletedOrExistingRoot returns the project root for path. A deleted project
// has no folder to look in, so its path is taken as the root.
func deletedOrExistingRoot(path, dir string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if _, err := os.Stat(path); err == nil {
		return project.Root(path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// printable returns text safe to print to a terminal. Labels and paths come
// from agents and projects, and control characters in them could rewrite what
// the terminal shows.
func printable(text string) string {
	if strings.IndexFunc(text, unicode.IsControl) < 0 {
		return text
	}
	return strconv.Quote(text)
}
