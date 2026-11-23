// Package fsnotify reports changes to directory trees as the operating
// system sees them: through FSEvents on macOS and inotify on Linux.
package fsnotify

import "errors"

// EventID orders the events of a volume. Only FSEvents has them, and a
// stream can resume after the last one it delivered.
type EventID uint64

// SinceNow starts a stream with the changes made from now on.
const SinceNow EventID = 0

// Event is a filesystem change reported by the operating system.
type Event struct {
	Path string
	// ID is the event's FSEvents ID. It is 0 on Linux.
	ID EventID
	// IsHistoryDone marks the end of the earlier changes a resumed stream
	// delivers. Events after it are new, and may arrive before it.
	IsHistoryDone bool
	// ShouldRescan is set when the system lost track of changes under Path or
	// dropped events. Everything under Path has to be compared in full.
	ShouldRescan bool
}

// ErrWatchLimit means the system ran out of watches. Only Linux has such a
// limit, because inotify needs a watch per directory.
var ErrWatchLimit = errors.New("inotify watch limit reached; raise it with: sudo sysctl fs.inotify.max_user_watches=1048576")
