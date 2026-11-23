package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

const startTimeout = 3 * time.Second

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

// Checkpoint asks the project's server to take a checkpoint, calling start
// first if no server is running. If the server cannot be reached, it scans
// the project instead, so a checkpoint is always taken, and prunes the store
// if that is due, since no server will.
func Checkpoint(s *store.Store, options store.CheckpointOptions, start StartFunc) (store.Checkpoint, error) {
	cp, err := checkpointThroughServer(s, options, start)
	if err == nil {
		return cp, nil
	}
	if start == nil {
		return s.Take(options)
	}
	s.LogError(fmt.Errorf("watcher unavailable, scanned the project instead: %w", err))
	cp, err = s.Take(options)
	if err != nil {
		return cp, err
	}
	if _, err := s.PruneIfDue(time.Now(), store.DefaultLimits); err != nil {
		s.LogError(fmt.Errorf("pruning failed: %w", err))
	}
	return cp, nil
}

func checkpointThroughServer(s *store.Store, options store.CheckpointOptions, start StartFunc) (store.Checkpoint, error) {
	conn, err := connect(s, start)
	if err != nil {
		return store.Checkpoint{}, err
	}
	resp, err := send(conn, request{Operation: operationCheckpoint, Options: options})
	if err != nil {
		return store.Checkpoint{}, err
	}
	if resp.Error != "" {
		return store.Checkpoint{}, errors.New(resp.Error)
	}
	return resp.Checkpoint, nil
}

// Status reports whether a server is running for s.
func Status(s *store.Store) (ServerStatus, error) {
	conn, err := connect(s, nil)
	if err != nil {
		return ServerStatus{}, nil
	}
	resp, err := send(conn, request{Operation: operationStatus})
	if err != nil {
		return ServerStatus{}, err
	}
	return ServerStatus{IsRunning: true, CheckpointCount: resp.CheckpointCount}, nil
}

func connect(s *store.Store, start StartFunc) (net.Conn, error) {
	path, err := socketPath(s)
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
			if err == nil {
				err = errors.New("exit status 0")
			}
			return nil, fmt.Errorf("the watcher exited: %w", err)
		case <-deadline:
			return nil, errors.New("the watcher did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func send(conn net.Conn, req request) (response, error) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return response{}, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	err := json.NewDecoder(conn).Decode(&resp)
	return resp, err
}
