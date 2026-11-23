package watch

import (
	"path"

	"github.com/shimlabs/undo/internal/tree"
)

// changeSet applies changes to a listing and records which paths changed.
// Removing everything under a directory is deferred to finish, so deleting
// many directories costs one pass over the listing instead of one each.
type changeSet struct {
	listing tree.Listing
	changed map[string]bool
	deleted map[string]bool
	seen    map[string]bool
	// goneDirs lose everything under them. replacedDirs keep the paths
	// found again in the directory that replaced them.
	goneDirs     map[string]bool
	replacedDirs map[string]bool
	// uncertainDirs are the directories where a name the listing did not
	// hold appeared or disappeared. On a filesystem that ignores case and
	// Unicode form, the listing may still hold that name in another
	// spelling.
	uncertainDirs map[string]bool
	// hasLinkCountChange is set when a listed path gained or lost a name.
	// The other name can be anywhere, and only a full comparison finds it.
	hasLinkCountChange bool
}

func newChangeSet(listing tree.Listing) *changeSet {
	return &changeSet{
		listing:       listing,
		changed:       map[string]bool{},
		deleted:       map[string]bool{},
		seen:          map[string]bool{},
		goneDirs:      map[string]bool{},
		replacedDirs:  map[string]bool{},
		uncertainDirs: map[string]bool{},
	}
}

func (c *changeSet) remove(rel string) {
	if _, ok := c.listing[rel]; ok {
		delete(c.listing, rel)
		c.deleted[rel] = true
		delete(c.changed, rel)
	}
}

// removeTree removes rel, and everything under it if it was a directory.
func (c *changeSet) removeTree(rel string, wasDir bool) {
	c.remove(rel)
	if wasDir {
		c.removeUnder(rel)
	}
}

// removeGone removes rel, which is gone. A path the listing does not hold
// may be listed in another spelling, so its directory becomes uncertain.
func (c *changeSet) removeGone(rel string, wasTracked, wasDir bool) {
	if wasTracked {
		c.removeTree(rel, wasDir)
	} else {
		c.uncertainDirs[path.Dir(rel)] = true
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

// removeUnder marks everything under the directory rel, which is gone, for
// removal by finish. Paths below it that a stat still reaches, through a
// spelling that differs only in case, are removed too.
func (c *changeSet) removeUnder(rel string) {
	c.goneDirs[rel] = true
}

// replaceUnder marks everything under the directory rel, which another
// directory replaced, for removal by finish, except the paths found again.
func (c *changeSet) replaceUnder(rel string) {
	c.replacedDirs[rel] = true
}

// finish removes the paths under the directories passed to removeUnder and
// replaceUnder.
func (c *changeSet) finish() {
	if len(c.goneDirs) == 0 && len(c.replacedDirs) == 0 {
		return
	}
	for rel := range c.listing {
		if rel == "." {
			continue
		}
		for dir := path.Dir(rel); ; dir = path.Dir(dir) {
			if c.goneDirs[dir] || c.replacedDirs[dir] && !c.seen[rel] {
				c.remove(rel)
				break
			}
			if dir == "." {
				break
			}
		}
	}
}
