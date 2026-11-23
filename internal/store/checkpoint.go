package store

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/tree"
	"github.com/shimlabs/undo/internal/walk"
)

// CheckpointOptions describe a checkpoint: the command that is about to run,
// where it runs, and which agent session runs it.
type CheckpointOptions struct {
	Label   string `json:"label"`
	Dir     string `json:"dir"`
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
}

// Checkpoint is a point the project can be restored to: its state just before
// the command in Label ran.
type Checkpoint struct {
	ID   int       `json:"id"`
	Time time.Time `json:"time"`
	CheckpointOptions
}

// Checkpointer takes a checkpoint of the project in projectStore, either by scanning it
// with Take or through the background watcher.
type Checkpointer func(projectStore *Store, options CheckpointOptions) (Checkpoint, error)

// Manifest is what a layer recorded: the state of every path that changed
// since the previous checkpoint and the paths that were deleted. Unstable
// files kept changing while they were cloned, and unreadable paths could not
// be saved at all. ExcludePatterns are the exclude rules the checkpoint was
// taken with, so a restore leaves alone what that checkpoint never saw.
type Manifest struct {
	Changed         map[string]tree.Entry `json:"changed"`
	Deleted         []string              `json:"deleted"`
	Unstable        []string              `json:"unstable,omitempty"`
	Unreadable      []string              `json:"unreadable,omitempty"`
	ExcludePatterns []string              `json:"exclude_patterns"`
}

// Changes are what a caller that tracks the project itself found since the
// latest checkpoint.
type Changes struct {
	// Listing is the full listing of the project. Commit updates it for files
	// that were written while they were cloned or could not be read.
	Listing tree.Listing
	Changed []string
	Deleted []string
	// Unreadable are directories the caller could not list.
	Unreadable []string
	// ExcludePatterns are the exclude rules the changes were found with.
	ExcludePatterns []string
}

// ErrStale means another checkpoint was taken after the one the changes were
// based on.
var ErrStale = errors.New("changes are not relative to the latest checkpoint")

const (
	cloneAttempts  = 3
	maxLabelLength = 200
)

// Take records the project's current state as a new checkpoint. The first
// checkpoint clones the whole project into the base. Later checkpoints scan
// the project and clone only the paths that changed since the previous one.
func (s *Store) Take(options CheckpointOptions) (Checkpoint, error) {
	unlock, err := s.lock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer unlock()

	matcher, err := exclude.New(s.root)
	if err != nil {
		return Checkpoint{}, err
	}
	latest, hasLatest, err := s.Latest()
	if err != nil {
		return Checkpoint{}, err
	}
	var previous tree.Listing
	if hasLatest {
		previous, err = s.ListingAt(latest.ID)
	} else {
		previous, err = s.cloneBase(matcher)
	}
	if err != nil {
		return Checkpoint{}, err
	}

	current, skipped, err := tree.Scan(s.root, matcher)
	if err != nil {
		return Checkpoint{}, err
	}
	changed, deleted := tree.Changed(previous, current)
	checkpoint, err := s.commit(latest.ID, options, Changes{
		Listing:         current,
		Changed:         changed,
		Deleted:         deleted,
		Unreadable:      skipped.Unreadable,
		ExcludePatterns: matcher.Patterns(),
	})
	if err != nil {
		return Checkpoint{}, err
	}
	if !s.IsListingBehind(checkpoint.ID) {
		return checkpoint, nil
	}
	return checkpoint, s.SaveListing(checkpoint.ID, current)
}

// Commit records a checkpoint from changes found by a caller that tracks the
// project itself, such as the watcher. The changes must be relative to
// checkpoint after, which must be the latest checkpoint.
func (s *Store) Commit(after int, options CheckpointOptions, changes Changes) (Checkpoint, error) {
	unlock, err := s.lock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer unlock()

	latest, hasLatest, err := s.Latest()
	if err != nil {
		return Checkpoint{}, err
	}
	if !hasLatest || latest.ID != after {
		return Checkpoint{}, ErrStale
	}
	return s.commit(after, options, changes)
}

// commit writes layer latestID+1 and then appends it to the index, so a
// checkpoint is only listed once its layer is complete. The manifest and the
// index line are flushed to disk; the cloned files are not, since flushing
// every file would make checkpoints far slower. After a power loss, the
// newest checkpoint's files can therefore be incomplete, while older
// checkpoints are unaffected.
func (s *Store) commit(latestID int, options CheckpointOptions, changes Changes) (Checkpoint, error) {
	id := latestID + 1
	// A layer left behind by an interrupted checkpoint would mix old files
	// into this one.
	if err := os.RemoveAll(s.layerDir(id)); err != nil {
		return Checkpoint{}, err
	}
	manifest, err := s.cloneChanged(id, changes)
	if err != nil {
		os.RemoveAll(s.layerDir(id))
		return Checkpoint{}, toCloneError(err)
	}
	if err := os.MkdirAll(s.layerDir(id), 0o700); err != nil {
		return Checkpoint{}, err
	}
	if err := writeJSON(s.manifestPath(id), manifest); err != nil {
		return Checkpoint{}, err
	}
	options.Label = shortLabel(options.Label)
	checkpoint := Checkpoint{ID: id, Time: time.Now(), CheckpointOptions: options}
	return checkpoint, s.appendIndex(checkpoint)
}

// shortLabel keeps the first line of label, cut to maxLabelLength
// characters. Agents often run commands with large heredocs, and the index
// holds every label.
func shortLabel(label string) string {
	label, _, isMultiline := strings.Cut(strings.TrimSpace(label), "\n")
	if utf8.RuneCountInString(label) > maxLabelLength {
		label = string([]rune(label)[:maxLabelLength])
		isMultiline = true
	}
	if isMultiline {
		label += " …"
	}
	return label
}

func toCloneError(err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return ErrCloneUnsupported
	}
	return err
}

// cloneBase clones the project into the base and returns the listing taken
// just before the clone. If a path changes while the clone runs, it no longer
// matches that listing, so the first checkpoint records it again. Excluded
// and unreadable paths are never read.
func (s *Store) cloneBase(matcher *exclude.Matcher) (tree.Listing, error) {
	before, skipped, err := tree.Scan(s.root, matcher)
	if err != nil {
		return nil, err
	}
	base := s.baseDir()
	if err := removeAll(base); err != nil {
		return nil, err
	}
	plan := basePlan{
		hasSkippedDescendant: map[string]bool{},
		children:             map[string][]string{},
		listing:              before,
	}
	for _, rel := range skipped.Excluded {
		plan.markAncestors(rel)
	}
	for _, rel := range skipped.Unreadable {
		plan.markAncestors(rel)
		plan.hasSkippedDescendant[rel] = true
	}
	for rel := range before {
		if rel != "." {
			plan.children[path.Dir(rel)] = append(plan.children[path.Dir(rel)], rel)
		}
	}
	if err := s.cloneInto(".", base, &plan); err != nil {
		return nil, errors.Join(toCloneError(err), removeAll(base))
	}
	// The listing records each directory's mode. In the base, read-only
	// directories would stop merges and keep the store from being deleted.
	for rel, entry := range before {
		if entry.Kind == tree.Dir && entry.Mode.Perm()&0o700 != 0o700 {
			if err := makeWritable(s.basePath(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	// A file created after the scan and removed before the first checkpoint
	// would otherwise stay in the base, and a restore that clones a whole
	// directory from the base would bring it back.
	if err := s.removeUnlisted(before); err != nil {
		return nil, err
	}
	if err := writeGob(s.baseListingPath(), before); err != nil {
		return nil, err
	}
	return before, nil
}

// removeUnlisted removes every path in the base that listing does not hold.
func (s *Store) removeUnlisted(listing tree.Listing) error {
	return walk.Dirs(".", func(dir string) ([]string, error) {
		entries, err := os.ReadDir(s.basePath(dir))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var subdirs []string
		for _, entry := range entries {
			rel := entry.Name()
			if dir != "." {
				rel = dir + "/" + entry.Name()
			}
			listed, isListed := listing[rel]
			switch {
			case !isListed:
				if err := removeAll(s.basePath(rel)); err != nil {
					return nil, err
				}
			case listed.Kind == tree.Dir && entry.IsDir():
				subdirs = append(subdirs, rel)
			}
		}
		return subdirs, nil
	})
}

// basePlan describes how to clone the project into the base: directories
// with skipped paths below them are created and filled child by child, and
// everything else is cloned in one call.
type basePlan struct {
	hasSkippedDescendant map[string]bool
	children             map[string][]string
	listing              tree.Listing
}

func (p *basePlan) markAncestors(rel string) {
	for dir := path.Dir(rel); !p.hasSkippedDescendant[dir]; dir = path.Dir(dir) {
		p.hasSkippedDescendant[dir] = true
		if dir == "." {
			return
		}
	}
}

// cloneInto clones rel to dst. A directory that cannot be cloned in one call
// because something inside it is unreadable is cloned child by child, and the
// unreadable files are left out of the listing.
func (s *Store) cloneInto(rel, dst string, plan *basePlan) error {
	if !plan.hasSkippedDescendant[rel] {
		err := s.clone(filepath.Join(s.root, filepath.FromSlash(rel)), dst)
		switch {
		case err == nil || errors.Is(err, fs.ErrNotExist):
			return nil
		case !errors.Is(err, fs.ErrPermission):
			return err
		case plan.listing[rel].Kind != tree.Dir:
			delete(plan.listing, rel)
			return nil
		}
		if err := removeAll(dst); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, child := range plan.children[rel] {
		if err := s.cloneInto(child, filepath.Join(dst, path.Base(child)), plan); err != nil {
			return err
		}
	}
	return nil
}

// cloneChanged copies each changed file and symlink into the layer for id.
func (s *Store) cloneChanged(id int, changes Changes) (Manifest, error) {
	manifest := Manifest{
		Changed:         map[string]tree.Entry{},
		Deleted:         changes.Deleted,
		Unreadable:      changes.Unreadable,
		ExcludePatterns: changes.ExcludePatterns,
	}
	if manifest.Deleted == nil {
		manifest.Deleted = []string{}
	}
	for _, rel := range changes.Changed {
		entry := changes.Listing[rel]
		if entry.Kind != tree.File && entry.Kind != tree.Symlink {
			manifest.Changed[rel] = entry
			continue
		}
		entry, isStable, err := s.cloneStable(id, rel, entry)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(changes.Listing, rel)
			manifest.Deleted = append(manifest.Deleted, rel)
			continue
		case errors.Is(err, fs.ErrPermission):
			delete(changes.Listing, rel)
			manifest.Unreadable = append(manifest.Unreadable, rel)
			continue
		case err != nil:
			return Manifest{}, err
		}
		if !isStable {
			manifest.Unstable = append(manifest.Unstable, rel)
		}
		changes.Listing[rel] = entry
		manifest.Changed[rel] = entry
	}
	return manifest, nil
}

// cloneStable clones rel into the layer and checks that the source did not
// change during the clone. If it did, it tries again a few times.
func (s *Store) cloneStable(id int, rel string, entry tree.Entry) (tree.Entry, bool, error) {
	src := filepath.Join(s.root, filepath.FromSlash(rel))
	dst := s.layerFile(id, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return entry, false, err
	}
	for attempt := 1; ; attempt++ {
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return entry, false, err
		}
		if err := s.clone(src, dst); err != nil {
			return entry, false, err
		}
		after, err := tree.Stat(src)
		if err != nil {
			return entry, false, err
		}
		if after == entry {
			return entry, true, nil
		}
		entry = after
		if attempt == cloneAttempts {
			// The stat can already include a write the clone missed. A change
			// time no file has makes the next checkpoint clone it again.
			entry.ChangeTime = 0
			return entry, false, nil
		}
	}
}
