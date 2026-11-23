package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

// watchCommand runs the project's watcher until it has been idle for an hour
// or is told to stop.
func watchCommand(env Env) error {
	projectStore, err := openStore(env, flags{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = watch.Serve(ctx, projectStore, watch.Options{SocketDir: env.SocketDir, IdleTimeout: time.Hour, Limits: store.DefaultLimits()})
	if err != nil && !errors.Is(err, watch.ErrRunning) {
		projectStore.LogError(fmt.Errorf("watcher stopped: %w", err))
		return err
	}
	return nil
}
