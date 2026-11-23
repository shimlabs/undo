package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/fsnotify"
)

// streamPosition is where events can resume: everything that changed after
// checkpoint CheckpointID was taken comes after event EventID.
type streamPosition struct {
	CheckpointID int    `json:"checkpoint_id"`
	EventID      uint64 `json:"event_id"`
	// EventStoreID identifies the volume's event history the ID belongs to.
	EventStoreID string `json:"event_store_id"`
}

// maxPositionAge is how old a saved position may be. The system trims its
// event history over time, and a gap in it would go unnoticed.
const maxPositionAge = 24 * time.Hour

// resume loads the listing at the latest checkpoint and returns the event to
// start after, when the saved position allows it. It returns SinceNow when
// the tracker has to compare the whole project instead.
func (t *tracker) resume() fsnotify.EventID {
	if !fsnotify.CanResume {
		return fsnotify.SinceNow
	}
	path := t.store.WatcherPositionPath()
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > maxPositionAge {
		return fsnotify.SinceNow
	}
	var position streamPosition
	if data, err := os.ReadFile(path); err != nil || json.Unmarshal(data, &position) != nil {
		return fsnotify.SinceNow
	}
	eventStoreID, err := fsnotify.EventStoreID(t.root)
	if err != nil || eventStoreID != position.EventStoreID {
		return fsnotify.SinceNow
	}
	latest, hasLatest, err := t.store.Latest()
	if err != nil || !hasLatest || position.CheckpointID > latest.ID {
		return fsnotify.SinceNow
	}
	if err := t.loadListing(latest.ID); err != nil {
		return fsnotify.SinceNow
	}
	t.shouldRescan = false
	t.position = position
	return fsnotify.EventID(position.EventID)
}

// savePosition records where a new tracker can resume.
func (t *tracker) savePosition() {
	if !fsnotify.CanResume || t.position.CheckpointID == 0 {
		return
	}
	eventStoreID, err := fsnotify.EventStoreID(t.root)
	if err != nil {
		return
	}
	t.position.EventStoreID = eventStoreID
	data, err := json.Marshal(t.position)
	if err == nil {
		err = atomicfile.WriteFile(t.store.WatcherPositionPath(), data, 0o600)
	}
	if err != nil {
		t.store.LogError(fmt.Errorf("saving the watcher position: %w", err))
	}
}

// startStream reads the exclude rules and starts watching with them. On Linux
// the stream does not watch excluded directories, so it has to start over
// when the rules change.
func (t *tracker) startStream(since fsnotify.EventID) error {
	matcher, err := exclude.New(t.root)
	if err != nil {
		return err
	}
	previous := t.matcher.Swap(matcher)
	stream, err := fsnotify.NewStream([]string{t.root, t.markerDir}, since, t.shouldSkip, t.handleEvents)
	if err != nil {
		t.matcher.Store(previous)
		return err
	}
	t.stream = stream
	return nil
}
