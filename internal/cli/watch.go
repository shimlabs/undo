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
func watchCommand(_ flags, env Env) error {
	projectStore, err := openStore(env, flags{})
	if err != nil {
		return err
	}
	defer projectStore.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = watch.Serve(ctx, projectStore, watch.Options{SocketDir: env.SocketDir, IdleTimeout: time.Hour, Limits: store.DefaultLimits()})
	if err != nil && !errors.Is(err, watch.ErrRunning) {
		projectStore.LogError(watcherStoppedError(err))
		return err
	}
	return nil
}

// watcherStoppedError is the error log's line for the watcher stopping with
// err, worded as undo words errors on screen.
func watcherStoppedError(err error) error {
	return fmt.Errorf("%w: %s", errWatcherStopped, describeError(err))
}

var errWatcherStopped = errors.New("the watcher stopped")
