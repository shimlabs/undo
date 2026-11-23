package watch

import (
	"context"
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

// pruneInterval is how often the server prunes the store. Pruning measures the
// whole store, which is too slow to do after every checkpoint.
var pruneInterval = time.Hour

// pruneQuiet is how long the server waits without requests before it prunes,
// so pruning does not compete with checkpoints for the disk.
var pruneQuiet = 30 * time.Second

// Options configure Serve.
type Options struct {
	// Idle is how long the server waits for a request before it exits.
	Idle time.Duration
	// Limits bound the history the server keeps when it prunes the store.
	Limits store.Limits
	// Ready, if set, is called once the server accepts connections.
	Ready func()
}

// Serve watches the project of s and takes checkpoints for clients until ctx
// is canceled or no request arrives for options.Idle. After checkpoints it
// prunes the store in the background once it has been quiet for pruneQuiet, at
// most once per pruneInterval.
func Serve(ctx context.Context, s *store.Store, options Options) error {
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
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()

	tracker, err := NewTracker(s)
	if err != nil {
		return err
	}
	defer tracker.Close()
	if options.Ready != nil {
		options.Ready()
	}

	prunes := make(chan struct{}, 1)
	pruned := make(chan struct{})
	go func() {
		defer close(pruned)
		for range prunes {
			s.Prune(time.Now(), options.Limits)
		}
	}()
	defer func() {
		close(prunes)
		<-pruned
	}()
	var lastPrune time.Time
	pruneDue := false

	lastID := 0
	for {
		wait := options.Idle
		if pruneDue {
			wait = min(wait, pruneQuiet)
		}
		if err := listener.SetDeadline(time.Now().Add(wait)); err != nil {
			return err
		}
		conn, err := listener.Accept()
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			if !pruneDue {
				return nil
			}
			pruneDue = false
			lastPrune = time.Now()
			select {
			case prunes <- struct{}{}:
			default:
			}
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		before := lastID
		lastID = serveOne(conn, tracker, lastID)
		if lastID != before && time.Since(lastPrune) >= pruneInterval {
			pruneDue = true
		}
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
