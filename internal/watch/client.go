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
	Running bool
	LastID  int
}

// Checkpoint asks the project's server to take a checkpoint, calling start
// first if no server is running. If the server cannot be reached, it scans
// the project instead, so a checkpoint is always taken.
func Checkpoint(s *store.Store, label, dir string, start func() error) (store.Checkpoint, error) {
	cp, err := checkpointThroughServer(s, label, dir, start)
	if err == nil {
		return cp, nil
	}
	if start != nil {
		s.LogError(fmt.Errorf("watcher unavailable, scanned the project instead: %w", err))
	}
	return s.Take(label, dir)
}

func checkpointThroughServer(s *store.Store, label, dir string, start func() error) (store.Checkpoint, error) {
	conn, err := connect(s, start)
	if err != nil {
		return store.Checkpoint{}, err
	}
	resp, err := send(conn, request{Op: "checkpoint", Label: label, Dir: dir})
	if err != nil {
		return store.Checkpoint{}, err
	}
	if resp.Error != "" {
		return store.Checkpoint{}, errors.New(resp.Error)
	}
	return resp.Checkpoint, nil
}

// Status reports whether a server is running for s and the last checkpoint
// it took.
func Status(s *store.Store) (ServerStatus, error) {
	conn, err := connect(s, nil)
	if err != nil {
		return ServerStatus{}, nil
	}
	resp, err := send(conn, request{Op: "status"})
	if err != nil {
		return ServerStatus{}, err
	}
	return ServerStatus{Running: true, LastID: resp.LastID}, nil
}

func connect(s *store.Store, start func() error) (net.Conn, error) {
	path, err := SocketPath(s)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil || start == nil {
		return conn, err
	}
	if err := start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if conn, err = net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			return conn, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("the watcher did not start")
}

func send(conn net.Conn, req request) (response, error) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(requestTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	err := json.NewDecoder(conn).Decode(&resp)
	return resp, err
}
