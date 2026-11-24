package store

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/parallel"
	"github.com/shimlabs/undo/internal/tree"
	"golang.org/x/sys/unix"
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
	CheckpointOptions

	ID   int       `json:"id"`
	Time time.Time `json:"time"`
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
	Changed  map[string]tree.Entry
	Deleted  []string
	Unstable []string
	// Unreadable is cumulative: every directory that could not be listed at
	// this checkpoint, not only new ones, plus the files that could not be
	// cloned now.
	Unreadable []string
	// Excluded are the excluded paths that were in the project, which no
	// checkpoint holds, so a restore can name the ones it cannot bring back.
	Excluded        []string
	ExcludePatterns []string
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
	// Excluded are the paths in the project that the exclude rules leave
	// out.
	Excluded []string
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
		Excluded:        skipped.Excluded,
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
	if err := s.removeAll(s.layerDir(id)); err != nil {
		return Checkpoint{}, err
	}
	manifest, err := s.cloneChanged(id, changes)
	if err != nil {
		return Checkpoint{}, errors.Join(toCloneError(err), s.removeAll(s.layerDir(id)))
	}
	if err := s.mkdirAll(s.layerDir(id)); err != nil {
		return Checkpoint{}, err
	}
	if err := s.writeGob(s.manifestPath(id), manifest); err != nil {
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
	if len(pending) == 0 {
		return changes.Changed
	}
	changed := slices.Clone(changes.Changed)
	isChanged := toSet(changed)
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

// cloneChanged copies each changed file and symlink into the layer for id.
func (s *Store) cloneChanged(id int, changes Changes) (Manifest, error) {
	manifest := Manifest{
		Changed:         map[string]tree.Entry{},
		Deleted:         changes.Deleted,
		Unreadable:      changes.Unreadable,
		Excluded:        withoutStoreDirs(changes.Excluded),
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
	for _, batch := range batchByDir(files, maxOpenDirs) {
		if err := s.cloneBatch(layer, files, batch, changes.Listing, clonedFiles); err != nil {
			return Manifest{}, err
		}
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

// cloneBatch clones files[i] for each i in batch into clonedFiles[i]. Many
// changed files share a directory, so each one is created and opened once,
// in the project and in the layer.
func (s *Store) cloneBatch(layer *os.Root, files []string, batch []int, listing tree.Listing, clonedFiles []clonedFile) error {
	dirs, err := openDirPairs(files, batch, func(dir string) (dirPair, error) { return s.openDirPair(layer, dir) })
	if err != nil {
		return err
	}
	defer closeDirPairs(dirs)
	return parallel.For(len(batch), func(j int) error {
		rel := files[batch[j]]
		pair, isOpen := dirs[path.Dir(rel)]
		if !isOpen {
			// A directory gone since the listing was made takes its files
			// with it, and they are recorded as deleted.
			clonedFiles[batch[j]] = clonedFile{err: fs.ErrNotExist}
			return nil
		}
		entry, isStable, err := s.cloneStable(pair, rel, listing[rel])
		clonedFiles[batch[j]] = clonedFile{entry, isStable, err}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return err
	})
}

// batchByDir groups the indexes of files so that each group holds the files
// of at most maxDirs directories.
func batchByDir(files []string, maxDirs int) [][]int {
	var dirs []string
	byDir := map[string][]int{}
	for i, rel := range files {
		dir := path.Dir(rel)
		if _, isSeen := byDir[dir]; !isSeen {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], i)
	}
	var batches [][]int
	for chunk := range slices.Chunk(dirs, maxDirs) {
		var batch []int
		for _, dir := range chunk {
			batch = append(batch, byDir[dir]...)
		}
		batches = append(batches, batch)
	}
	return batches
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
	if err := s.mkdirAll(s.layerFile(id, ".")); err != nil {
		return nil, err
	}
	return s.openRoot(s.layerFile(id, "."))
}

// dirPair is one directory open in the project and in the store, in a layer
// or the base.
type dirPair struct {
	project *os.File
	stored  *os.File
}

// openDirPairs opens, with open, the directory of paths[i] for each i in
// batch, each directory once. A directory gone since it was listed is left
// out.
func openDirPairs(paths []string, batch []int, open func(dir string) (dirPair, error)) (map[string]dirPair, error) {
	dirs := map[string]dirPair{}
	for _, i := range batch {
		dir := path.Dir(paths[i])
		if _, isOpen := dirs[dir]; isOpen {
			continue
		}
		pair, err := open(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			closeDirPairs(dirs)
			return nil, err
		}
		dirs[dir] = pair
	}
	return dirs, nil
}

func closeDirPairs(dirs map[string]dirPair) {
	for _, pair := range dirs {
		pair.close()
	}
}

func (p dirPair) close() {
	p.project.Close()
	p.stored.Close()
}

// openDirPair creates the directory dir in layer and opens it there and in
// the project.
func (s *Store) openDirPair(layer *os.Root, dir string) (dirPair, error) {
	projectDir, err := tree.OpenDir(s.root, dir)
	if err != nil {
		return dirPair{}, err
	}
	if err := layer.MkdirAll(dir, 0o700); err != nil {
		return dirPair{}, errors.Join(err, projectDir.Close())
	}
	layerDir, err := layer.Open(dir)
	if err != nil {
		return dirPair{}, errors.Join(err, projectDir.Close())
	}
	return dirPair{project: projectDir, stored: layerDir}, nil
}

// cloneStable clones rel from the project into the layer, through the
// directory pair that holds it, and checks that the source did not change
// during the clone. If it did, it tries again a few times.
func (s *Store) cloneStable(dirs dirPair, rel string, entry tree.Entry) (tree.Entry, bool, error) {
	name := path.Base(rel)
	for attempt := 1; ; attempt++ {
		// The layer starts empty, so only a retry has an earlier clone to
		// remove.
		if attempt > 1 {
			if err := unix.Unlinkat(int(dirs.stored.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return entry, false, &os.PathError{Op: "unlinkat", Path: name, Err: err}
			}
		}
		if err := s.cloneAt(dirs.project, name, dirs.stored, name); err != nil {
			return entry, false, err
		}
		after, err := tree.StatAt(dirs.project, name)
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

// withoutStoreDirs drops the stores undo keeps next to projects, which are
// excluded but are not the project's own.
func withoutStoreDirs(excluded []string) []string {
	return slices.DeleteFunc(slices.Clone(excluded), func(rel string) bool {
		return strings.HasPrefix(path.Base(rel), storeDirPrefix)
	})
}
