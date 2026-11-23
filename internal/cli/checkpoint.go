package cli

import (
	"os/exec"
	"strings"

	"github.com/shimlabs/undo/internal/store"
)

func checkpointCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	projectStore, err := createStore(env, parsed)
	if err != nil {
		return err
	}
	if parsed.isCopyMode {
		if err := projectStore.SetCopyMode(true); err != nil {
			return err
		}
	}
	checkpoint, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: parsed.label, Dir: env.Dir})
	if err != nil {
		return err
	}
	env.stdout().Success("Took checkpoint %d", checkpoint.ID)
	return nil
}

func runCommand(args []string, env Env) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usageError{"run needs a command"}
	}
	projectStore, err := createStore(env, flags{})
	if err != nil {
		return err
	}
	if _, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: strings.Join(args, " "), Dir: env.Dir}); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}
