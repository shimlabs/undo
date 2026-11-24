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
	case errors.Is(err, project.ErrNotProject):
		errOut.Errorf("%s", sentence(notProjectMessage(err)))
		errOut.Printf("Run %s, or start your agent, inside a project folder.\n", errOut.Bold("undo"))
	default:
		printError(errOut, err)
	}
	return 1
}

// projectRoot returns the root of the project in env.Dir, or of the project
// that --project names. Either may have been deleted, as when the shell is
// still in a folder an agent removed.
func projectRoot(env Env, parsed flags) (string, error) {
	if parsed.project != "" {
		return deletedOrExistingRoot(parsed.project, env.Dir)
	}
	if _, err := os.Stat(env.Dir); errors.Is(err, fs.ErrNotExist) {
		return deletedRootAround(env.StoreHome, env.Dir)
	}
	return project.Root(env.Dir)
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

func absolutePath(path, dir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// deletedRootAround returns the root of the deleted project that held dir:
// the nearest folder at or above dir with a store. Without one, dir is
// taken as the root.
func deletedRootAround(home, dir string) (string, error) {
	existing := filepath.Dir(dir)
	for !exists(existing) && existing != filepath.Dir(existing) {
		existing = filepath.Dir(existing)
	}
	// Stores record resolved roots, so the part of dir that still exists is
	// resolved the way they were.
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	rest, err := filepath.Rel(existing, dir)
	if err != nil {
		return "", err
	}
	dir = filepath.Join(resolved, rest)
	for candidate := dir; candidate != resolved; candidate = filepath.Dir(candidate) {
		if projectStore, err := store.Open(home, candidate); err == nil {
			projectStore.Close()
			return candidate, nil
		}
	}
	return dir, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// deletedOrExistingRoot returns the project root for path. A deleted project
// has no folder to look in, so its path is taken as the root.
func deletedOrExistingRoot(path, dir string) (string, error) {
	path = absolutePath(path, dir)
	if _, err := os.Stat(path); err == nil {
		return project.Root(path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s %w", displayPath(path), errProjectAndParentMissing)
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
