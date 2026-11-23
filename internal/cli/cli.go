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
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

// Version is set when the binary is built.
var Version = "dev"

const usageFormat = `undo puts your project back the way it was before an agent's command.

Usage:
  undo                     restore the state before the last command
  undo list                show checkpoints
  undo diff <id>           show what restoring checkpoint <id> would change
  undo to <id>             restore the state at checkpoint <id>
  undo run -- <command>    take a checkpoint, then run <command>
  undo checkpoint          take a checkpoint now
  undo status              show what undo protects
  undo install <agent>     checkpoint before every change the agent makes:
                           %s
  undo uninstall <agent>   remove that hook
  undo version             print the version

Flags:
  --yes                    restore without asking
  --label <text>           label for undo checkpoint
  --copy                   keep full copies, for filesystems without clones
  --project <path>         use the project at <path>, even one that was deleted
`

// printUsage writes the usage text with its section names in bold.
func printUsage(out *terminal.Writer) {
	text := usage()
	for _, section := range []string{"Usage:", "Flags:"} {
		text = strings.Replace(text, section, out.Bold(section), 1)
	}
	out.Printf("%s", text)
}

func usage() string {
	names := hook.AgentNames()
	last := len(names) - 1
	return fmt.Sprintf(usageFormat, strings.Join(names[:last], ", ")+" or "+names[last])
}

func (env Env) stdout() *terminal.Writer {
	return terminal.NewWriter(env.Stdout)
}

func (env Env) stderr() *terminal.Writer {
	return terminal.NewWriter(env.Stderr)
}

// Env holds the directories, streams and settings a command works with.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// StoreHome holds the stores of all projects.
	StoreHome string
	// SocketDir holds the watchers' sockets.
	SocketDir string
	// Executable is the path of the undo binary, which installed hooks run.
	Executable string
	// StartWatcher starts a background watcher for the project at root and
	// returns a channel that receives its error if it exits. When it is nil,
	// checkpoints scan the project instead.
	StartWatcher func(root string) (exited <-chan error, err error)
}

// watcher returns the client for the projects' watchers.
func (env Env) watcher() watch.Client {
	return watch.Client{SocketDir: env.SocketDir, Start: env.StartWatcher}
}

// checkpointer takes checkpoints through the project's watcher when one can
// be started, and by scanning the project otherwise.
func (env Env) checkpointer() store.Checkpointer {
	return env.watcher().Checkpoint
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
	errOut := env.stderr()
	if usageErr, ok := errors.AsType[usageError](err); ok {
		errOut.Error("%s", usageErr.message)
		errOut.Println("")
		printUsage(errOut)
		return 2
	}
	switch {
	case errors.Is(err, errDeclined):
	case errors.Is(err, store.ErrCloneUnsupported):
		errOut.Error("%s, so undo cannot protect this project cheaply.", terminal.Printable(err.Error()))
		errOut.Printf("Run %s once to keep full copies instead. They take as much disk as the project.\n", errOut.Bold("undo checkpoint --copy"))
	case errors.Is(err, project.ErrNotProject):
		errOut.Error("%s.", terminal.Printable(err.Error()))
		errOut.Println("Run undo, or start your agent, inside a project folder.")
	default:
		errOut.Error("%s", terminal.Printable(err.Error()))
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
		printUsage(env.stdout())
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

// projectRoot returns the root of the project in env.Dir, or of the project
// that --project names, which may have been deleted.
func projectRoot(env Env, parsed flags) (string, error) {
	if parsed.project == "" {
		return project.Root(env.Dir)
	}
	return deletedOrExistingRoot(parsed.project, env.Dir)
}

// openStore opens the existing store of the project. It returns an error
// matching fs.ErrNotExist if the project has no checkpoints yet.
func openStore(env Env, parsed flags) (*store.Store, error) {
	root, err := projectRoot(env, parsed)
	if err != nil {
		return nil, err
	}
	return store.Open(env.StoreHome, root)
}

// createStore opens the store of the project, creating it if needed.
func createStore(env Env, parsed flags) (*store.Store, error) {
	root, err := projectRoot(env, parsed)
	if err != nil {
		return nil, err
	}
	return store.Create(env.StoreHome, root)
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
