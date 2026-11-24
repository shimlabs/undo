// Package cli is the undo command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"

	"github.com/shimlabs/undo/internal/exclude"
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
	if commandErr, ok := errors.AsType[commandError](err); ok {
		printError(errOut, commandErr)
		return commandErr.exitCode
	}
	switch {
	case errors.Is(err, errDeclined) || errors.Is(err, errAlreadyReported):
	case errors.Is(err, store.ErrCloneUnsupported):
		printError(errOut, withHint(fmt.Errorf("%w, so undo cannot protect this project cheaply", err),
			fmt.Sprintf("Run %s once to keep full copies instead. They take as much disk as the project.", errOut.Bold("undo checkpoint --copy"))))
	case isUnreadableIgnoreFile(err):
		printError(errOut, withHint(err, fmt.Sprintf("Run %s in the project folder, then try again.", errOut.Bold("chmod u+r "+exclude.IgnoreFile))))
	case errors.Is(err, project.ErrNotProject):
		errOut.Errorf("%s", sentence(notProjectMessage(err)))
		printNotProjectHint(errOut)
	default:
		printError(errOut, err)
	}
	return 1
}

// isUnreadableIgnoreFile reports whether err is the project's .undoignore
// refusing to open for lack of permission.
func isUnreadableIgnoreFile(err error) bool {
	pathErr, ok := errors.AsType[*fs.PathError](err)
	return ok && errors.Is(err, fs.ErrPermission) && filepath.Base(pathErr.Path) == exclude.IgnoreFile
}

// printNotProjectHint says where undo works, after saying a folder is not a
// project.
func printNotProjectHint(out *terminal.Writer) {
	out.Printf("Run %s inside a project folder.\n", out.Bold("undo"))
}
