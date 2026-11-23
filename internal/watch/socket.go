package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// maxSocketPath is the longest socket path every supported system accepts.
const maxSocketPath = 100

// dialTimeout is how long to wait for a running server to accept.
const dialTimeout = 200 * time.Millisecond

// socketIDLength is how much of the project ID a socket name keeps, short
// enough to fit the platform's limit on socket paths.
const socketIDLength = 16

// DefaultSocketDir returns the directory for the servers' sockets:
// $UNDO_SOCKET_DIR if it is set, and otherwise a directory in the user's
// private temporary directory. Socket paths are short, so they cannot live in
// the store.
func DefaultSocketDir() string {
	if dir := os.Getenv("UNDO_SOCKET_DIR"); dir != "" {
		return dir
	}
	dir := filepath.Join(userRuntimeDir(), fmt.Sprintf("undo-%d", os.Getuid()))
	if len(dir)+len("/.sock")+socketIDLength > maxSocketPath {
		return fmt.Sprintf("/tmp/undo-%d", os.Getuid())
	}
	return dir
}

// socketPath returns the socket the server for projectStore listens on.
func socketPath(socketDir string, projectStore *store.Store) (string, error) {
	if err := checkSocketDir(socketDir); err != nil {
		return "", err
	}
	id := project.ID(projectStore.Root())[:socketIDLength]
	return filepath.Join(socketDir, id+".sock"), nil
}

// checkSocketDir creates dir if needed and makes sure only the current user
// can use it. It refuses a directory that another user created or that is a
// symlink, so nobody else can take the server's place.
func checkSocketDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: %w", dir, ErrUnsafeSocketDir)
	}
	return nil
}

// ErrUnsafeSocketDir means the directory for the watchers' sockets is not
// a directory owned by the current user and private to them.
var ErrUnsafeSocketDir = errors.New("must be a directory owned by you and private to you")

var (
	errNotUnixSocket = errors.New("not a unix socket")
	errOtherUser     = errors.New("the other end of the socket runs as user")
)

// dial connects to the socket at path, waiting at most dialTimeout.
func dial(ctx context.Context, path string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	return dialer.DialContext(ctx, "unix", path)
}

// dialServer connects to the server at path and checks that it runs as the
// current user.
func dialServer(path string) (net.Conn, error) {
	// A client has no context of its own; dialTimeout bounds the wait.
	conn, err := dial(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if err := checkPeer(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// checkPeer refuses a connection from or to another user.
func checkPeer(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return errNotUnixSocket
	}
	uid, err := peerUID(unixConn)
	if err != nil {
		return err
	}
	if uid != os.Getuid() {
		return fmt.Errorf("%w %d", errOtherUser, uid)
	}
	return nil
}
