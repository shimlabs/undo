package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

const (
	startTimeout = 3 * time.Second
	// startBackoff is how long checkpoints scan the project instead of
	// starting the server again after it failed to start. Starting it walks
	// the project, and at the Linux watch limit it fails every time.
	startBackoff = 10 * time.Minute
	// startFailureFile records in the store why the server last failed to
	// start.
	startFailureFile = "watcher-failure"
)

// ServerStatus describes the server for a project.
type ServerStatus struct {
	IsRunning bool
	// CheckpointCount is how many checkpoints the server took since it
	// started.
	CheckpointCount int
}

// StartFunc starts a server for the project in the background. The channel
// it returns receives the server's error if it exits.
type StartFunc func() (exited <-chan error, err error)

// Client talks to the servers in SocketDir.
type Client struct {
	SocketDir string
	// Start, if set, starts a server for a project that has none.
	Start func(root string) (exited <-chan error, err error)
}

// Checkpoint asks the project's server to take a checkpoint, starting one if
// none is running. If the server cannot be reached, it scans the project
// instead, so a checkpoint is always taken, and prunes the store if that is
// due, since no server will.
func (c Client) Checkpoint(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
	start := c.startFor(projectStore)
	checkpoint, err := c.checkpointThroughServer(projectStore, options, start)
	if err == nil {
		return checkpoint, nil
	}
	if c.Start == nil {
		return projectStore.Take(options)
	}
	if start != nil {
		projectStore.LogError(fmt.Errorf("watcher unavailable, scanned the project instead: %w", err))
	}
	checkpoint, err = projectStore.Take(options)
	if err != nil {
		return checkpoint, err
	}
	if _, err := projectStore.PruneIfDue(time.Now(), store.DefaultLimits()); err != nil {
		projectStore.LogError(fmt.Errorf("pruning failed: %w", err))
	}
	return checkpoint, nil
}

// startFor returns the function that starts the project's server, or nil
// while an earlier start failure is recent.
func (c Client) startFor(projectStore *store.Store) StartFunc {
	if c.Start == nil {
		return nil
	}
	if _, isRecent := StartFailure(projectStore); isRecent {
		return nil
	}
	return func() (<-chan error, error) {
		exited, err := c.Start(projectStore.Root())
		if err != nil {
			return nil, err
		}
		recorded := make(chan error, 1)
		go func() {
			if err, ok := <-exited; ok {
				recordStartFailure(projectStore, err)
				recorded <- err
			}
		}()
		return recorded, nil
	}
}

// StartFailure returns why the project's server last failed to start, and
// whether that was recent enough that checkpoints still scan instead.
func StartFailure(projectStore *store.Store) (reason string, isRecent bool) {
	path := projectStore.FilePath(startFailureFile)
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), time.Since(info.ModTime()) < startBackoff
}

func recordStartFailure(projectStore *store.Store, failure error) {
	if failure == nil {
		failure = errors.New("exit status 0")
	}
	if err := os.WriteFile(projectStore.FilePath(startFailureFile), []byte(failure.Error()), 0o600); err != nil {
		projectStore.LogError(err)
	}
}

func (c Client) checkpointThroughServer(projectStore *store.Store, options store.CheckpointOptions, start StartFunc) (store.Checkpoint, error) {
	conn, err := c.connect(projectStore, start)
	if err != nil {
		return store.Checkpoint{}, err
	}
	reply, err := send(conn, serverRequest{Operation: operationCheckpoint, Options: options})
	if err != nil {
		return store.Checkpoint{}, err
	}
	if reply.Error != "" {
		return store.Checkpoint{}, errors.New(reply.Error)
	}
	return reply.Checkpoint, nil
}

// Status reports whether a server is running for projectStore.
func (c Client) Status(projectStore *store.Store) (ServerStatus, error) {
	conn, err := c.connect(projectStore, nil)
	if err != nil {
		// No server answers, so none is running.
		return ServerStatus{}, nil
	}
	reply, err := send(conn, serverRequest{Operation: operationStatus})
	if err != nil {
		return ServerStatus{}, err
	}
	return ServerStatus{IsRunning: true, CheckpointCount: reply.CheckpointCount}, nil
}

func (c Client) connect(projectStore *store.Store, start StartFunc) (net.Conn, error) {
	path, err := socketPath(c.SocketDir, projectStore)
	if err != nil {
		return nil, err
	}
	conn, err := dialServer(path)
	if err == nil || start == nil {
		return conn, err
	}
	exited, err := start()
	if err != nil {
		return nil, err
	}
	deadline := time.After(startTimeout)
	for {
		if conn, err := dialServer(path); err == nil {
			return conn, nil
		}
		select {
		case err := <-exited:
			return nil, fmt.Errorf("the watcher exited: %w", err)
		case <-deadline:
			return nil, errors.New("the watcher did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func send(conn net.Conn, request serverRequest) (serverResponse, error) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return serverResponse{}, err
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return serverResponse{}, err
	}
	var reply serverResponse
	err := json.NewDecoder(conn).Decode(&reply)
	return reply, err
}
