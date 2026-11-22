package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/shimlabs/undo/internal/store"
)

// SocketPath returns the socket the server for s listens on. Socket paths
// are limited to about 100 bytes, so they live in a short per-user directory
// under /tmp instead of in the store.
func SocketPath(s *store.Store) (string, error) {
	dir := os.Getenv("UNDO_SOCKET_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/tmp/undo-%d", os.Getuid())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must be owned by you and private to you", dir)
	}
	id := filepath.Base(s.Dir())
	if len(id) > 16 {
		id = id[:16]
	}
	return filepath.Join(dir, id+".sock"), nil
}
