package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

// ErrRunning means a server for the project is already running.
var ErrRunning = errors.New("a watcher is already running for this project")

const requestTimeout = 30 * time.Second

// defaultPruneQuietPeriod is how long the server waits without requests
// before it prunes, so pruning does not compete with checkpoints for the disk.
const defaultPruneQuietPeriod = 30 * time.Second

const (
	operationCheckpoint = "checkpoint"
	operationStatus     = "status"
)

type request struct {
	Operation string                  `json:"op"`
	Options   store.CheckpointOptions `json:"options"`
}

type response struct {
	Checkpoint      store.Checkpoint `json:"checkpoint"`
	CheckpointCount int              `json:"checkpoint_count"`
	Error           string           `json:"error,omitempty"`
}

// Options configure Serve.
type Options struct {
	// IdleTimeout is how long the server waits for a request before it exits.
	IdleTimeout time.Duration
	// Limits bound the history the server keeps when it prunes the store.
	Limits store.Limits
	// PruneQuietPeriod is how long the server waits without requests before
	// it prunes. Zero means 30 seconds.
	PruneQuietPeriod time.Duration
	// Ready, if set, is called once the server accepts connections.
	Ready func()
}

// Serve watches the project of s and takes checkpoints for clients until ctx
// is canceled or no request arrives for options.IdleTimeout. After
// checkpoints, once it has been quiet for options.PruneQuietPeriod, it prunes
// the store in the background if pruning is due.
func Serve(ctx context.Context, s *store.Store, options Options) error {
	if options.PruneQuietPeriod == 0 {
		options.PruneQuietPeriod = defaultPruneQuietPeriod
	}
	listener, err := listen(s)
	if err != nil {
		return err
	}
	defer listener.Close()
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
	pruner := startPruner(s, options.Limits)
	defer pruner.stop()

	server := server{listener: listener, tracker: tracker, pruner: pruner, options: options}
	err = server.acceptLoop()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// listen opens the project's socket, unless another server already answers
// on it.
func listen(s *store.Store) (*net.UnixListener, error) {
	path, err := socketPath(s)
	if err != nil {
		return nil, err
	}
	if conn, err := net.DialTimeout("unix", path, dialTimeout); err == nil {
		conn.Close()
		return nil, ErrRunning
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// pruner prunes the store on a background goroutine, one request at a time.
// A request made while a prune runs waits for it rather than being dropped.
type pruner struct {
	requests chan struct{}
	running  sync.WaitGroup
}

func startPruner(s *store.Store, limits store.Limits) *pruner {
	p := &pruner{requests: make(chan struct{}, 1)}
	p.running.Go(func() {
		for range p.requests {
			if _, err := s.PruneIfDue(time.Now(), limits); err != nil {
				s.LogError(fmt.Errorf("pruning failed: %w", err))
			}
		}
	})
	return p
}

func (p *pruner) request() {
	select {
	case p.requests <- struct{}{}:
	default:
	}
}

func (p *pruner) stop() {
	close(p.requests)
	p.running.Wait()
}

type server struct {
	listener        *net.UnixListener
	tracker         *Tracker
	pruner          *pruner
	options         Options
	checkpointCount int
}

// acceptLoop serves connections until the server has been idle for
// options.IdleTimeout. After a checkpoint, the first quiet period triggers a
// prune.
func (s *server) acceptLoop() error {
	isPruneDue := false
	for {
		wait := s.options.IdleTimeout
		if isPruneDue {
			wait = min(wait, s.options.PruneQuietPeriod)
		}
		if err := s.listener.SetDeadline(time.Now().Add(wait)); err != nil {
			return err
		}
		conn, err := s.listener.Accept()
		if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
			if !isPruneDue {
				return nil
			}
			isPruneDue = false
			s.pruner.request()
			continue
		}
		if err != nil {
			return err
		}
		if s.handleConn(conn) {
			isPruneDue = true
		}
	}
}

// handleConn answers one request and reports whether it took a checkpoint.
func (s *server) handleConn(conn net.Conn) bool {
	defer conn.Close()
	if checkPeer(conn) != nil || conn.SetDeadline(time.Now().Add(requestTimeout)) != nil {
		return false
	}
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return false
	}
	var resp response
	isCheckpointed := false
	switch req.Operation {
	case operationCheckpoint:
		cp, err := s.tracker.Checkpoint(req.Options)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Checkpoint = cp
			s.checkpointCount++
			isCheckpointed = true
		}
	case operationStatus:
	default:
		resp.Error = "unknown request " + req.Operation
	}
	resp.CheckpointCount = s.checkpointCount
	// A client that does not get the reply scans the project instead.
	_ = json.NewEncoder(conn).Encode(resp)
	return isCheckpointed
}
