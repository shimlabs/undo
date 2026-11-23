package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shimlabs/undo/internal/atomicfile"
	"github.com/shimlabs/undo/internal/fsnotify"
)

// streamPosition is where events can resume: everything that changed after
// checkpoint CheckpointID was taken comes after event EventID.
type streamPosition struct {
	CheckpointID int    `json:"checkpoint_id"`
	EventID      uint64 `json:"event_id"`
	// EventStoreID identifies the volume's event history the ID belongs to.
	EventStoreID string    `json:"event_store_id"`
	SavedAt      time.Time `json:"saved_at"`
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
	var position streamPosition
	data, err := os.ReadFile(t.store.WatcherPositionPath())
	if err != nil || json.Unmarshal(data, &position) != nil || time.Since(position.SavedAt) > maxPositionAge {
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
	t.position.SavedAt = time.Now()
	data, err := json.Marshal(t.position)
	if err == nil {
		err = atomicfile.WriteFile(t.store.WatcherPositionPath(), data, 0o600)
	}
	if err != nil {
		t.store.LogError(fmt.Errorf("saving the watcher position: %w", err))
	}
}
