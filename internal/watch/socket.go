package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/store"
)

// maxSocketPath is the longest socket path every supported system accepts.
const maxSocketPath = 100

// dialTimeout is how long to wait for a running server to accept.
const dialTimeout = 200 * time.Millisecond

// socketPath returns the socket the server for s listens on. Socket paths
// are short, so they live in a private per-user directory instead of in the
// store.
func socketPath(s *store.Store) (string, error) {
	dir, err := socketDir()
	if err != nil {
		return "", err
	}
	id := filepath.Base(s.Dir())
	if len(id) > 16 {
		id = id[len(id)-16:]
	}
	return filepath.Join(dir, id+".sock"), nil
}

// socketDir returns a directory only the current user can use. It refuses a
// directory that another user created or that is a symlink, so nobody else
// can take the server's place.
func socketDir() (string, error) {
	dir := os.Getenv("UNDO_SOCKET_DIR")
	if dir == "" {
		dir = filepath.Join(userRuntimeDir(), fmt.Sprintf("undo-%d", os.Getuid()))
		if len(dir)+len("/0123456789abcdef.sock") > maxSocketPath {
			dir = fmt.Sprintf("/tmp/undo-%d", os.Getuid())
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must be a directory owned by you and private to you", dir)
	}
	return dir, nil
}

// dialServer connects to the server at path and checks that it runs as the
// current user.
func dialServer(path string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", path, dialTimeout)
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
		return errors.New("not a unix socket")
	}
	uid, err := peerUID(unixConn)
	if err != nil {
		return err
	}
	if uid != os.Getuid() {
		return fmt.Errorf("the other end of the socket runs as user %d", uid)
	}
	return nil
}
