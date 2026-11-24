package restore

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/tree"
	"github.com/shimlabs/undo/internal/watch"
)

// randomSeeds is how many random histories each run tries. UNDO_RANDOM_SEEDS
// raises it for a long search.
func randomSeeds(t *testing.T) int {
	t.Helper()
	if seeds, err := strconv.Atoi(os.Getenv("UNDO_RANDOM_SEEDS")); err == nil {
		return seeds
	}
	if testing.Short() {
		return 3
	}
	return 25
}

// randomSeedList returns the seeds to try: UNDO_RANDOM_SEED alone, to replay
// a failure, or the first randomSeeds.
func randomSeedList(t *testing.T) iter.Seq[uint64] {
	t.Helper()
	if seed, err := strconv.ParseUint(os.Getenv("UNDO_RANDOM_SEED"), 10, 64); err == nil {
		return slices.Values([]uint64{seed})
	}
	return func(yield func(uint64) bool) {
		for seed := range uint64(randomSeeds(t)) {
			if !yield(seed) {
				return
			}
		}
	}
}

// runRandomHistory makes random changes to a project, takes a checkpoint
// after each, and now and then restores a random checkpoint or prunes old
// ones. Every restore must leave the project exactly as it was at that
// checkpoint.
func runRandomHistory(t *testing.T, seed uint64, projectStore *store.Store, checkpointer store.Checkpointer, isScanning bool) {
	t.Helper()
	root := projectStore.Root()
	mutator := testutil.NewMutator(t, root, seed, testutil.MutatorOptions{ShouldLinkOutside: isScanning})
	random := rand.New(rand.NewPCG(seed, seed+1))
	states := map[int]map[string]string{}
	var log []string
	checkpoint := func(label string) {
		before := testutil.Snapshot(t, root, testutil.SnapshotOptions{ShouldSkipExcluded: true})
		taken, err := checkpointer(projectStore, store.CheckpointOptions{Label: label, Dir: root})
		if err != nil {
			t.Fatalf("seed %d: checkpoint after %v: %v", seed, log, err)
		}
		states[taken.ID] = before
		log = append(log, "#"+strconv.Itoa(taken.ID))
		if differences := listingDifferences(t, projectStore, taken.ID, isScanning); differences != "" {
			t.Fatalf("seed %d: %v\nstored listing of checkpoint %d differs from a scan:\n%s", seed, log, taken.ID, differences)
		}
	}
	restoreRandom := func() {
		checkpoints, err := projectStore.List()
		if err != nil {
			t.Fatal(err)
		}
		target := checkpoints[random.IntN(len(checkpoints))]
		log = append(log, "undo to "+strconv.Itoa(target.ID))
		before := testutil.Snapshot(t, root, testutil.SnapshotOptions{ShouldSkipExcluded: true})
		latestBefore, _, err := projectStore.Latest()
		if err != nil {
			t.Fatal(err)
		}
		changes, err := Apply(projectStore, target.ID, checkpointer)
		if err != nil {
			t.Fatalf("seed %d: %v\nrestore failed: %v", seed, log, err)
		}
		if latest, _, err := projectStore.Latest(); err == nil {
			if isScanning && latest.ID > latestBefore.ID+1 {
				t.Fatalf("seed %d: %v\nthe restore's scanned safety checkpoint %d did not cover the project:\n%s",
					seed, log, latestBefore.ID+1, listingDifferences(t, projectStore, latestBefore.ID+1, true))
			}
			for id := latestBefore.ID + 1; id <= latest.ID; id++ {
				states[id] = before
				log = append(log, "#"+strconv.Itoa(id))
			}
		}
		want, got := states[target.ID], testutil.Snapshot(t, root, testutil.SnapshotOptions{ShouldSkipExcluded: true})
		if differences := unexpectedDifferences(t, projectStore, target.ID, changes, want, got, isScanning); len(differences) > 0 {
			t.Fatalf("seed %d: %v\nkept %v, lost %v, skipped %v\nproject differs from checkpoint %d:\n%s",
				seed, log, changes.Kept, changes.Lost, changes.Skipped, target.ID, describeDifferences(want, got, differences))
		}
	}
	checkpoint("start")
	for step := range 40 {
		switch roll := random.IntN(20); {
		case roll < 3:
			restoreRandom()
		case roll == 3:
			log = append(log, "prune")
			if _, err := projectStore.Prune(time.Now(), store.Limits{MaxAge: time.Hour, MaxCount: 4, MaxBytes: 1 << 40}); err != nil {
				t.Fatalf("seed %d: %v\nprune: %v", seed, log, err)
			}
		default:
			change := mutator.Mutate()
			log = append(log, change)
			checkpoint("step " + strconv.Itoa(step))
		}
	}
	for range 5 {
		restoreRandom()
	}
}

func TestRandomHistoryWithScans(t *testing.T) {
	for seed := range randomSeedList(t) {
		root := testutil.TempDir(t)
		projectStore, err := store.Create(t.TempDir(), root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { projectStore.Close() })
		runRandomHistory(t, seed, projectStore, (*store.Store).Take, true)
	}
}

func describeDifferences(want, got map[string]string, paths []string) string {
	var lines strings.Builder
	for _, rel := range paths {
		fmt.Fprintf(&lines, "  %s: want %.80q, got %.80q\n", rel, want[rel], got[rel])
	}
	return lines.String()
}

// unexpectedDifferences returns the paths where the project, got, differs
// from checkpoint id, want, other than those the restore kept. A restore
// also leaves alone what the checkpoint could not read, as it does excluded
// paths. Without isExact, named pipes are left out, as listingDifferences
// leaves them out.
func unexpectedDifferences(t *testing.T, projectStore *store.Store, id int, changes Changes, want, got map[string]string, isExact bool) []string {
	t.Helper()
	manifest, err := projectStore.Manifest(id)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(testutil.SnapshotDiff(want, got), func(rel string) bool {
		if !isExact && (strings.HasPrefix(want[rel], "fifo ") || strings.HasPrefix(got[rel], "fifo ")) {
			return true
		}
		rel = filepath.ToSlash(rel)
		return isAtOrInside(rel, changes.Kept) || isAtOrInside(rel, manifest.Unreadable) || isAroundKept(rel, want, got, changes.Kept)
	})
}

// isAroundKept reports whether rel is a directory the target state lacks
// that the restore left in place because it holds a kept path.
func isAroundKept(rel string, want, got map[string]string, kept []string) bool {
	if _, isWanted := want[rel]; isWanted || !strings.HasPrefix(got[rel], "dir ") {
		return false
	}
	return slices.ContainsFunc(kept, func(keptPath string) bool {
		return strings.HasPrefix(keptPath, rel+"/")
	})
}

// listingDifferences compares the listing the store builds for checkpoint id
// with a scan of the project as it is now. Files the checkpoint could not
// read are left out of the scan, as they are of the listing. Without isExact,
// named pipes are left out too, with the times of their directories:
// FSEvents does not report them on every version of macOS, and the watcher
// learns of them at its next full comparison, as design.md says.
func listingDifferences(t *testing.T, projectStore *store.Store, id int, isExact bool) string {
	t.Helper()
	matcher, err := exclude.New(projectStore.Root())
	if err != nil {
		t.Fatal(err)
	}
	scanned, skipped, err := tree.Scan(projectStore.Root(), matcher)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := projectStore.ListingAt(id)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := projectStore.Manifest(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range manifest.Unreadable {
		if _, isStored := stored[rel]; !isStored {
			delete(scanned, rel)
		}
	}
	if !isExact {
		ignoreNamedPipes(stored, scanned)
	}
	changed, deleted := tree.Changed(stored, scanned)
	var lines strings.Builder
	for _, rel := range changed {
		fmt.Fprintf(&lines, "  %s: stored %+v, scanned %+v\n", rel, stored[rel], scanned[rel])
	}
	for _, rel := range deleted {
		fmt.Fprintf(&lines, "  %s: stored %+v, not in the scan\n", rel, stored[rel])
	}
	if !slices.Equal(manifest.Excluded, skipped.Excluded) {
		fmt.Fprintf(&lines, "  excluded paths: recorded %v, scanned %v\n", manifest.Excluded, skipped.Excluded)
	}
	return lines.String()
}

// ignoreNamedPipes leaves named pipes out of both listings, and the times of
// the directories that hold one, which change with a pipe the events missed.
func ignoreNamedPipes(listings ...tree.Listing) {
	holdsPipe := map[string]bool{}
	for _, listing := range listings {
		for rel, entry := range listing {
			if entry.Kind == tree.Other {
				holdsPipe[path.Dir(rel)] = true
			}
		}
	}
	for _, listing := range listings {
		maps.DeleteFunc(listing, func(_ string, entry tree.Entry) bool { return entry.Kind == tree.Other })
		for dir := range holdsPipe {
			if entry, ok := listing[dir]; ok {
				entry.ModTime, entry.ChangeTime = 0, 0
				listing[dir] = entry
			}
		}
	}
}

func TestRandomHistoryWithWatcher(t *testing.T) {
	for seed := range randomSeedList(t) {
		root := testutil.TempDir(t)
		projectStore, err := store.Create(t.TempDir(), root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { projectStore.Close() })
		client := startWatcher(t, projectStore)
		checkpointer := func(projectStore *store.Store, options store.CheckpointOptions) (store.Checkpoint, error) {
			checkpoint, err := client.Checkpoint(projectStore, options)
			if line := projectStore.LastError(); line != "" {
				t.Fatalf("seed %d: the checkpoint did not go through the watcher: %s", seed, line)
			}
			return checkpoint, err
		}
		runRandomHistory(t, seed, projectStore, checkpointer, false)
	}
}

// startWatcher runs a watcher for projectStore until the test ends, and
// returns a client that only uses it. Pruning is left to the test.
func startWatcher(t *testing.T, projectStore *store.Store) watch.Client {
	t.Helper()
	socketDir := testutil.SocketDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	exited := make(chan error, 1)
	go func() {
		exited <- watch.Serve(ctx, projectStore, watch.Options{
			SocketDir:        socketDir,
			IdleTimeout:      time.Hour,
			Limits:           store.Limits{MaxAge: 24 * time.Hour, MaxCount: 1 << 20, MaxBytes: 1 << 40},
			PruneQuietPeriod: time.Hour,
			Ready:            func() { close(ready) },
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-exited; err != nil {
			t.Errorf("watcher: %v", err)
		}
	})
	select {
	case <-ready:
	case err := <-exited:
		t.Fatalf("watcher stopped early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not start")
	}
	return watch.Client{SocketDir: socketDir, Start: func(string) (<-chan error, error) { return nil, errWatcherGone }}
}

var errWatcherGone = errors.New("the test's watcher is gone")
