package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/shell"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
)

func checkpointCommand(parsed flags, env Env) error {
	if parsed.isCopyMode {
		// Copy mode stays on for good, so where clones work it only costs
		// disk.
		root, err := existingProjectRoot(env, parsed)
		if err != nil {
			return err
		}
		if clone.IsSupportedIn(root) {
			return usageError{message: "this filesystem can clone, so --copy is not needed here"}
		}
	}
	projectStore, err := createStore(env, parsed)
	if err != nil {
		return err
	}
	defer projectStore.Close()
	isCopyModeNew := parsed.isCopyMode && !projectStore.IsCopyMode()
	if isCopyModeNew {
		if err := projectStore.SetCopyMode(true); err != nil {
			return err
		}
	}
	label := parsed.label
	if label == "" {
		label = "undo checkpoint"
	}
	checkpoint, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: label, Dir: env.Dir})
	if err != nil {
		return err
	}
	if isCopyModeNew {
		env.stdout().Successf("Took checkpoint %d. Checkpoints of this project now keep full copies, which take as much disk as the project.", checkpoint.ID)
		return nil
	}
	env.stdout().Successf("Took checkpoint %d.", checkpoint.ID)
	return nil
}

func runCommand(parsed flags, env Env) error {
	args := parsed.args
	if len(args) == 0 {
		return usageError{message: "undo run needs a command to run, after --"}
	}
	if err := checkRunnable(env.Dir, args[0]); err != nil {
		return err
	}
	projectStore, err := createStore(env, flags{})
	if err != nil {
		return err
	}
	defer projectStore.Close()
	if _, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: shell.Join(args), Dir: env.Dir}); err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // undo run runs the command the user typed
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}

// checkRunnable fails the way a shell does when name, as run in dir, cannot
// be found or run, before any checkpoint is taken for it.
func checkRunnable(dir, name string) error {
	if !strings.Contains(name, "/") {
		if _, err := exec.LookPath(name); err != nil {
			return commandError{err: fmt.Errorf("%w: %s", errCommandNotFound, name), exitCode: 127}
		}
		return nil
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return commandError{err: fmt.Errorf("%w: %s", errNoSuchFile, name), exitCode: 127}
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return commandError{
			err:      withHint(fmt.Errorf("%s %w", name, errNotExecutable), "Run chmod +x "+terminal.Printable(shell.Join([]string{name}))+" to make it executable."),
			exitCode: 126,
		}
	}
	return nil
}

var (
	errCommandNotFound = errors.New("command not found")
	errNoSuchFile      = errors.New("no such file")
	errNotExecutable   = errors.New("is not executable")
)
