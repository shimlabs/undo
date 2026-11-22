package watch

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

// ErrRunning means a server for the project is already running.
var ErrRunning = errors.New("a watcher is already running for this project")

const requestTimeout = 30 * time.Second

type request struct {
	Op    string `json:"op"`
	Label string `json:"label,omitempty"`
	Dir   string `json:"dir,omitempty"`
}

type response struct {
	Checkpoint store.Checkpoint `json:"checkpoint"`
	LastID     int              `json:"last_id"`
	Error      string           `json:"error,omitempty"`
}

// Serve watches the project of s and takes checkpoints for clients until no
// request arrives for idle. ready, if set, is called once it accepts
// connections.
func Serve(s *store.Store, idle time.Duration, ready func()) error {
	path, err := SocketPath(s)
	if err != nil {
		return err
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		return ErrRunning
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}

	tracker, err := NewTracker(s)
	if err != nil {
		return err
	}
	defer tracker.Close()
	if ready != nil {
		ready()
	}

	lastID := 0
	for {
		if err := listener.SetDeadline(time.Now().Add(idle)); err != nil {
			return err
		}
		conn, err := listener.Accept()
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil
		}
		if err != nil {
			return err
		}
		lastID = serveOne(conn, tracker, lastID)
	}
}

func serveOne(conn net.Conn, tracker *Tracker, lastID int) int {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(requestTimeout))
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return lastID
	}
	var resp response
	switch req.Op {
	case "checkpoint":
		cp, err := tracker.Checkpoint(req.Label, req.Dir)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Checkpoint = cp
			lastID = cp.ID
		}
	case "status":
	default:
		resp.Error = "unknown request " + req.Op
	}
	resp.LastID = lastID
	json.NewEncoder(conn).Encode(resp)
	return lastID
}
