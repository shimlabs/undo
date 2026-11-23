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
	"github.com/shimlabs/undo/internal/parallel"
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
	// UnstableCount is how many files changed while the checkpoint saved
	// them.
	UnstableCount int `json:"unstable_count,omitempty"`
	// StoredBytes is at most the disk space the checkpoint's own copies take.
	StoredBytes int64 `json:"stored_bytes,omitempty"`
}

// Checkpointer takes a checkpoint of the project in projectStore, either by
// scanning it with Take or through the background watcher.
type Checkpointer func(projectStore *Store, options CheckpointOptions) (Checkpoint, error)

// Manifest is what a layer recorded: the state of every path that changed
// since the previous checkpoint and the paths that were deleted. Unstable
// files kept changing while they were cloned, and unreadable paths could not
// be saved at all. ExcludePatterns are the exclude rules the checkpoint was
// taken with, so a restore leaves alone what that checkpoint never saw.
type Manifest struct {
	Changed  map[string]tree.Entry `json:"changed"`
	Deleted  []string              `json:"deleted"`
	Unstable []string              `json:"unstable,omitempty"`
	// Unreadable is cumulative: every directory that could not be listed at
	// this checkpoint, not only new ones, plus the files that could not be
	// cloned now.
	Unreadable      []string `json:"unreadable,omitempty"`
	ExcludePatterns []string `json:"exclude_patterns"`
}

// Changes are what a caller that tracks the project itself found since the
// latest checkpoint.
type Changes struct {
	// Listing is the full listing of the project. Commit updates it for files
	// that were written while they were cloned or could not be read.
	Listing tree.Listing
	Changed []string
	Deleted []string
	// Unreadable are all the directories the caller cannot list now, not
	// only new ones.
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
	record, err := s.readLost()
	if err != nil {
		return Checkpoint{}, err
	}
	changes.Changed = withPending(changes, record.Pending)
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
	checkpoint := Checkpoint{
		ID:                id,
		Time:              time.Now(),
		CheckpointOptions: options,
		UnstableCount:     len(manifest.Unstable),
		StoredBytes:       storedBytes(manifest.Changed),
	}
	if err := s.appendIndex(checkpoint); err != nil {
		return Checkpoint{}, err
	}
	// After a crash before this, the next commit stores the pending paths
	// once more.
	if len(record.Pending) > 0 {
		record.Pending = nil
		if err := s.writeLost(record); err != nil {
			return checkpoint, err
		}
	}
	return checkpoint, nil
}

// withPending adds to the changed paths the pending ones the project still
// holds, so the checkpoint stores them again.
func withPending(changes Changes, pending []string) []string {
	changed := changes.Changed
	isChanged := setOf(changed)
	for _, rel := range pending {
		if _, isListed := changes.Listing[rel]; isListed && !isChanged[rel] {
			changed = append(changed, rel)
		}
	}
	return changed
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
	files := splitChanged(changes, &manifest)
	if len(files) == 0 {
		return manifest, nil
	}
	clonedFiles := make([]clonedFile, len(files))
	layer, err := s.openLayer(id)
	if err != nil {
		return Manifest{}, err
	}
	defer layer.Close()
	// Many changed files share a directory, so each one is created once.
	dirs := map[string]bool{}
	for _, rel := range files {
		if dir := path.Dir(rel); !dirs[dir] {
			dirs[dir] = true
			if err := layer.MkdirAll(dir, 0o700); err != nil {
				return Manifest{}, err
			}
		}
	}
	err = parallel.For(len(files), func(i int) error {
		entry, isStable, err := s.cloneStable(layer, id, files[i], changes.Listing[files[i]])
		clonedFiles[i] = clonedFile{entry, isStable, err}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return err
	})
	if err != nil {
		return Manifest{}, err
	}
	for i, rel := range files {
		switch cloned := clonedFiles[i]; {
		case errors.Is(cloned.err, fs.ErrNotExist):
			delete(changes.Listing, rel)
			manifest.Deleted = append(manifest.Deleted, rel)
		case errors.Is(cloned.err, fs.ErrPermission):
			// The file's older content is not what the project holds now, so
			// later checkpoints must not bring it back either.
			delete(changes.Listing, rel)
			manifest.Deleted = append(manifest.Deleted, rel)
			manifest.Unreadable = append(manifest.Unreadable, rel)
		default:
			if !cloned.isStable {
				manifest.Unstable = append(manifest.Unstable, rel)
			}
			changes.Listing[rel] = cloned.entry
			manifest.Changed[rel] = cloned.entry
		}
	}
	return manifest, nil
}

// clonedFile is how cloning one changed file went.
type clonedFile struct {
	entry    tree.Entry
	isStable bool
	err      error
}

// splitChanged records the changed directories and other entries without
// content in manifest, and returns the files and symlinks to clone. A path
// below a symlink or a file cannot be in the project, and cloning it would
// go through the link, so it is recorded as deleted.
func splitChanged(changes Changes, manifest *Manifest) (files []string) {
	for _, rel := range changes.Changed {
		entry := changes.Listing[rel]
		switch {
		case !hasDirParents(changes.Listing, rel):
			delete(changes.Listing, rel)
			manifest.Deleted = append(manifest.Deleted, rel)
		case entry.Kind == tree.File || entry.Kind == tree.Symlink:
			files = append(files, rel)
		default:
			manifest.Changed[rel] = entry
		}
	}
	return files
}

// hasDirParents reports whether every parent of rel is a directory in
// listing.
func hasDirParents(listing tree.Listing, rel string) bool {
	for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
		if listing[dir].Kind != tree.Dir {
			return false
		}
	}
	return true
}

// openLayer creates the files directory of layer id and opens it as a root,
// so creating parents and removing files there never follows a symlink that
// was cloned into the layer.
func (s *Store) openLayer(id int) (*os.Root, error) {
	if err := os.MkdirAll(s.layerFile(id, "."), 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.layerFile(id, "."))
}

// cloneStable clones rel into the layer, whose directory for rel must exist,
// and checks that the source did not change during the clone. If it did, it
// tries again a few times.
func (s *Store) cloneStable(layer *os.Root, id int, rel string, entry tree.Entry) (tree.Entry, bool, error) {
	src := filepath.Join(s.root, filepath.FromSlash(rel))
	dst := s.layerFile(id, rel)
	for attempt := 1; ; attempt++ {
		// The layer starts empty, so only a retry has an earlier clone to
		// remove.
		if attempt > 1 {
			if err := layer.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return entry, false, err
			}
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
