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

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

func checkpointCommand(args []string, env Env) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	s, err := openStore(env)
	if err != nil {
		return err
	}
	if f.copy {
		if err := s.SetCopyMode(true); err != nil {
			return err
		}
	}
	cp, err := takeCheckpoint(s, f.label, env.Dir, env)
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
	s, err := openStore(env)
	if err != nil {
		return err
	}
	if _, err := takeCheckpoint(s, strings.Join(args, " "), env.Dir, env); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}

// takeCheckpoint checkpoints through the project's watcher when one can be started,
// and by scanning the project otherwise.
func takeCheckpoint(s *store.Store, label, dir string, env Env) (store.Checkpoint, error) {
	if env.StartWatcher == nil {
		return s.Take(label, dir)
	}
	return watch.Checkpoint(s, label, dir, func() error { return env.StartWatcher(s.Root()) })
}

func watchCommand(env Env) error {
	s, err := openStore(env)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := watch.Serve(ctx, s, watch.Options{Idle: time.Hour, Limits: store.DefaultLimits}); err != nil && !errors.Is(err, watch.ErrRunning) {
		s.LogError(fmt.Errorf("watcher stopped: %w", err))
		return err
	}
	return nil
}
