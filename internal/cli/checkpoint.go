package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

func checkpointCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return err
	}
	if parsed.isCopyMode {
		if err := projectStore.SetCopyMode(true); err != nil {
			return err
		}
	}
	cp, err := env.checkpointer()(projectStore, store.CheckpointOptions{Label: parsed.label, Dir: env.Dir})
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Checkpoint %d\n", cp.ID)
	return nil
}

func runCommand(args []string, env Env) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usageError{"run needs a command"}
	}
	projectStore, err := openStore(env, flags{})
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

// hookCommand handles one event from an agent. It never fails and never
// prints, because some agents block the tool when a hook fails or prints. An
// unknown agent name is handled like any other agent, and Handle logs its
// failures in the store.
func hookCommand(args []string, env Env) {
	defer func() { _ = recover() }()
	var agent hook.Agent
	if len(args) > 0 {
		agent, _ = hook.FindAgent(args[0])
	}
	_ = agent.Handle(env.Stdin, env.checkpointer())
}

func watchCommand(env Env) error {
	projectStore, err := openStore(env, flags{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = watch.Serve(ctx, projectStore, watch.Options{IdleTimeout: time.Hour, Limits: store.DefaultLimits})
	if err != nil && !errors.Is(err, watch.ErrRunning) {
		projectStore.LogError(fmt.Errorf("watcher stopped: %w", err))
		return err
	}
	return nil
}
