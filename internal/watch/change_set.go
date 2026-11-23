package watch

import (
	"path"

	"github.com/shimlabs/undo/internal/tree"
)

// changeSet applies changes to a listing and records which paths changed.
// Removing everything under a directory is deferred to finish, so deleting
// many directories costs one pass over the listing instead of one each.
type changeSet struct {
	listing     tree.Listing
	changed     map[string]bool
	deleted     map[string]bool
	seen        map[string]bool
	removedDirs map[string]bool
}

func newChangeSet(listing tree.Listing) *changeSet {
	return &changeSet{
		listing:     listing,
		changed:     map[string]bool{},
		deleted:     map[string]bool{},
		seen:        map[string]bool{},
		removedDirs: map[string]bool{},
	}
}

func (c *changeSet) remove(rel string) {
	if _, ok := c.listing[rel]; ok {
		delete(c.listing, rel)
		c.deleted[rel] = true
		delete(c.changed, rel)
	}
}

func (c *changeSet) update(rel string, entry tree.Entry) {
	c.seen[rel] = true
	if previous, ok := c.listing[rel]; !ok || previous != entry {
		c.listing[rel] = entry
		c.changed[rel] = true
		delete(c.deleted, rel)
	}
}

// removeUnder marks everything under rel for removal by finish.
func (c *changeSet) removeUnder(rel string) {
	c.removedDirs[rel] = true
}

// finish removes the paths under the directories passed to removeUnder,
// except paths that were found again since, such as the files of a
// directory that replaced a removed one.
func (c *changeSet) finish() {
	if len(c.removedDirs) == 0 {
		return
	}
	for rel := range c.listing {
		if rel == "." || c.seen[rel] {
			continue
		}
		for dir := path.Dir(rel); ; dir = path.Dir(dir) {
			if c.removedDirs[dir] {
				c.remove(rel)
				break
			}
			if dir == "." {
				break
			}
		}
	}
}
