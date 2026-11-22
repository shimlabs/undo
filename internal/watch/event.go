// Package watch follows changes to a project as they happen, so a checkpoint
// only needs to look at the paths that changed.
package watch

// Event is a filesystem change reported by the operating system.
type Event struct {
	Path string
	// Rescan is set when the system lost track of changes under Path, or
	// dropped events. The project then has to be compared in full.
	Rescan bool
}
