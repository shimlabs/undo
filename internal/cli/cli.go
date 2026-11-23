// Package cli is the undo command line.
package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

// Version is set when the binary is built.
var Version = "dev" //nolint:gochecknoglobals // the linker sets it with -X

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
		errOut.Errorf("%s, so undo cannot protect this project cheaply.", terminal.Printable(err.Error()))
		errOut.Printf("Run %s once to keep full copies instead. They take as much disk as the project.\n", errOut.Bold("undo checkpoint --copy"))
	case errors.Is(err, project.ErrNotProject):
		errOut.Errorf("%s.", terminal.Printable(err.Error()))
		errOut.Println("Run undo, or start your agent, inside a project folder.")
	default:
		errOut.Errorf("%s", terminal.Printable(err.Error()))
	}
	return 1
}

// printUsageError writes err, how its command is typed, and where to read
// more.
func printUsageError(out *terminal.Writer, err usageError) {
	out.Errorf("%s", terminal.Printable(err.message))
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
