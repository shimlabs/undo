package watch

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// userRuntimeDir returns $XDG_RUNTIME_DIR, which systemd creates for each
// user with mode 0700, or the system temporary directory without it.
func userRuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var credentialsErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialsErr != nil {
		return 0, credentialsErr
	}
	return int(credentials.Uid), nil
}

// canEventsArriveLate says whether an event can be delivered after the
// marker written after it. inotify delivers one stream's events in order, and
// the marker folder is in the same stream as the project.
const canEventsArriveLate = false
