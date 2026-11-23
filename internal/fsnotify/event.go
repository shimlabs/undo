// Package fsnotify reports changes to directory trees as the operating
// system sees them: through FSEvents on macOS and inotify on Linux.
package fsnotify

import "errors"

// Event is a filesystem change reported by the operating system.
type Event struct {
	Path string
	// ShouldRescan is set when the system lost track of changes under Path or
	// dropped events. Everything under Path has to be compared in full.
	ShouldRescan bool
}

// ErrWatchLimit means the system ran out of watches. Only Linux has such a
// limit, because inotify needs a watch per directory.
var ErrWatchLimit = errors.New("inotify watch limit reached; raise it with: sudo sysctl fs.inotify.max_user_watches=1048576")
