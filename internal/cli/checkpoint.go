package cli

import (
	"context"
	"os/exec"
	"strings"

	"github.com/shimlabs/undo/internal/store"
)

func checkpointCommand(parsed flags, env Env) error {
	projectStore, err := createStore(env, parsed)
	if err != nil {
		return err
	}
	defer projectStore.Close()
	if parsed.isCopyMode {
		if err := projectStore.SetCopyMode(true); err != nil {
			return err
		}
	}
	checkpoint, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: parsed.label, Dir: env.Dir})
	if err != nil {
		return err
	}
	env.stdout().Successf("Took checkpoint %d.", checkpoint.ID)
	return nil
}

func runCommand(parsed flags, env Env) error {
	args := parsed.args
	if len(args) == 0 {
		return usageError{message: "undo run needs a command to run, after --"}
	}
	projectStore, err := createStore(env, flags{})
	if err != nil {
		return err
	}
	defer projectStore.Close()
	if _, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: strings.Join(args, " "), Dir: env.Dir}); err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // undo run runs the command the user typed
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}
