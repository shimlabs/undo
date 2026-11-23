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
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

// Version is set when the binary is built.
var Version = "dev"

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

// usageError is a mistake in how undo was called. command, when set, is the
// command whose usage the error shows.
type usageError struct {
	message string
	command *command
}

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
		printUsageError(errOut, usageErr)
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
	name, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, rest = args[0], args[1:]
	}
	if name == "" && len(rest) > 0 && (rest[0] == "--version" || rest[0] == "-v") {
		return versionCommand(nil, env)
	}
	cmd, ok := findCommand(name)
	if !ok {
		return unknownCommandError(name)
	}
	isHelp, err := checkArguments(cmd, rest)
	if err != nil {
		return err
	}
	if isHelp {
		if cmd.name == "" {
			printHelp(env.stdout())
		} else {
			printCommandHelp(env.stdout(), cmd)
		}
		return nil
	}
	err = cmd.run(rest, env)
	if usageErr, ok := errors.AsType[usageError](err); ok && usageErr.command == nil {
		usageErr.command = &cmd
		return usageErr
	}
	return err
}

// checkArguments reports whether args ask for help, and refuses flags cmd
// does not take and more arguments than it takes. Commands that run another
// command, or that agents and the watcher run, take whatever they get.
func checkArguments(cmd command, args []string) (isHelp bool, err error) {
	if cmd.group == "" && cmd.name != "help" && cmd.name != "version" {
		return false, nil
	}
	if cmd.maxArguments < 0 {
		return len(args) > 0 && (args[0] == "-h" || args[0] == "--help"), nil
	}
	count := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return true, nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			count++
			continue
		}
		name, _, hasValue := strings.Cut(arg, "=")
		flag, isKnown := findFlag(name)
		if !isKnown {
			return false, usageError{message: fmt.Sprintf("unknown flag %s", name), command: &cmd}
		}
		if !slices.Contains(cmd.flags, flag.name) {
			return false, usageError{message: fmt.Sprintf("%s does not take %s", cmd.listing(), flag.name), command: &cmd}
		}
		if flag.value != "" && !hasValue {
			i++
		}
	}
	if count > cmd.maxArguments {
		if cmd.maxArguments == 0 {
			return false, usageError{message: fmt.Sprintf("%s takes no arguments", cmd.listing()), command: &cmd}
		}
		return false, usageError{message: fmt.Sprintf("too many arguments for %s", cmd.listing()), command: &cmd}
	}
	return false, nil
}

// printUsageError writes err, how its command is typed, and where to read
// more.
func printUsageError(out *terminal.Writer, err usageError) {
	out.Error("%s", terminal.Printable(err.message))
	if err.command == nil {
		out.Printf("Run %s for the list of commands.\n", out.Bold("undo help"))
		return
	}
	out.Printf("%s %s\n", out.Bold("Usage:"), err.command.usageLine())
	help := "undo help " + err.command.name
	if err.command.name == "" {
		help = "undo help"
	}
	out.Printf("Run %s for more.\n", out.Bold(strings.TrimSpace(help)))
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
					return parsed, usageError{message: name + " needs a value"}
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
			return parsed, usageError{message: fmt.Sprintf("unknown flag %s", arg)}
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
