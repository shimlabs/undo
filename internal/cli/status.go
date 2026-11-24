package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

func statusCommand(parsed flags, env Env) error {
	root, err := existingProjectRoot(env, parsed)
	if err != nil {
		return err
	}
	matcher, err := exclude.New(root)
	if err != nil {
		return err
	}
	out := env.stdout()
	out.Headingf("%s", displayPath(root))
	projectStore, err := store.Open(env.StoreHome, root)
	switch {
	case isMissingProject(err):
		printNoCheckpointsField(out, root, false)
	case err != nil:
		return err
	default:
		defer projectStore.Close()
		if err := printStore(out, env, projectStore); err != nil {
			return err
		}
	}
	installed, unreadable := agentHooks()
	switch {
	case len(installed) > 0:
		printField(out, "Hooks", strings.Join(installed, ", "))
	case len(unreadable) == 0:
		printField(out, "Hooks", "none yet; run "+out.Bold("undo install <agent>")+" to add one")
	}
	for _, problem := range unreadable {
		printField(out, "Hooks", out.Yellow(problem))
	}
	printField(out, "Excluded", terminal.Printable(strings.Join(matcher.Patterns(), ", ")))
	printField(out, "Not covered", "anything outside this folder: databases, git push, API calls")
	return nil
}

// printNoCheckpointsField says how to take the first checkpoint.
func printNoCheckpointsField(out *terminal.Writer, root string, isCopyMode bool) {
	if !isCopyMode && !clone.IsSupportedIn(root) {
		printField(out, "Checkpoints", "none yet; this filesystem cannot clone, so run "+out.Bold("undo checkpoint --copy"))
		return
	}
	printField(out, "Checkpoints", "none yet; run "+out.Bold("undo checkpoint")+" to take one")
}

// printField writes one labeled line of undo status.
func printField(out *terminal.Writer, label, value string) {
	out.Printf("%s %s\n", out.Bold(fmt.Sprintf("%-12s", label)), value)
}

// printStore prints what the project's store holds and how it is kept.
func printStore(out *terminal.Writer, env Env, projectStore *store.Store) error {
	diskUsage, err := projectStore.UsageEstimate()
	if err != nil {
		return err
	}
	watcher, err := env.watcher().Status(projectStore)
	if err != nil {
		return err
	}
	printField(out, "Store", displayPath(projectStore.Dir()))
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if err := printCheckpointSummary(out, projectStore, checkpoints); err != nil {
		return err
	}
	switch {
	case projectStore.IsCopyMode():
		printField(out, "Storage", "full copies, because copy mode is on")
	case clone.IsSupportedIn(projectStore.Root()):
		printField(out, "Storage", "clones that share data with the project")
	}
	printField(out, "Disk used", fmt.Sprintf("about %s not shared with the project, limit %s", formatBytes(diskUsage), formatBytes(store.DefaultLimits().MaxBytes)))
	switch reason, isRecent := watch.StartFailure(projectStore); {
	case watcher.IsRunning:
		printField(out, "Watcher", out.Green("running")+fmt.Sprintf(", %s since it started", pluralize(watcher.CheckpointCount, "checkpoint")))
	case isRecent:
		printField(out, "Watcher", out.Red("could not start")+", so checkpoints scan the project: "+terminal.Printable(reason))
	default:
		printField(out, "Watcher", out.Yellow("not running")+", so checkpoints scan the project")
	}
	if line := projectStore.LastError(); line != "" && isNewerThanLatest(line, checkpoints) {
		printField(out, "Last error", out.Red(logLine(line)))
	}
	return nil
}

// isNewerThanLatest reports whether the log line was written after the
// latest checkpoint started. A failure a later checkpoint got past no longer
// matters.
func isNewerThanLatest(line string, checkpoints []store.Checkpoint) bool {
	moment, err := logTime(line)
	if err != nil || len(checkpoints) == 0 {
		return true
	}
	// Log times are whole seconds.
	return !moment.Before(checkpoints[len(checkpoints)-1].Time.Truncate(time.Second))
}

// logTime returns the time a line of the error log was written.
func logTime(line string) (time.Time, error) {
	stamp, _, _ := strings.Cut(line, " ")
	return time.Parse(time.RFC3339, stamp)
}

// printCheckpointSummary prints how many checkpoints there are, what the
// latest one could not save, and how many hold files that were changing while
// they were saved.
func printCheckpointSummary(out *terminal.Writer, projectStore *store.Store, checkpoints []store.Checkpoint) error {
	if len(checkpoints) == 0 {
		printNoCheckpointsField(out, projectStore.Root(), projectStore.IsCopyMode())
		return nil
	}
	printField(out, "Checkpoints", fmt.Sprintf("%s, the oldest %s", formatCount(len(checkpoints)), timeAgo(checkpoints[0].Time)))
	var unstableCount int
	for _, checkpoint := range checkpoints {
		if checkpoint.UnstableCount > 0 {
			unstableCount++
		}
	}
	latest, err := projectStore.Manifest(checkpoints[len(checkpoints)-1].ID)
	if err != nil {
		return err
	}
	if len(latest.Unreadable) > 0 {
		names := make([]string, len(latest.Unreadable))
		for i, rel := range latest.Unreadable {
			info, err := os.Lstat(filepath.Join(projectStore.Root(), filepath.FromSlash(rel)))
			names[i] = displayRel(rel, err == nil && info.IsDir())
		}
		reason := pluralWord(len(names), " (undo has no permission to read it, so it is not covered)", " (undo has no permission to read these, so they are not covered)")
		printField(out, "Unreadable", out.Yellow(strings.Join(names, ", "))+reason)
	}
	if unstableCount > 0 {
		printField(out, "Changing", out.Yellow(pluralize(unstableCount, "checkpoint"))+" hold files that changed while they were saved; a restore lists them")
	}
	return nil
}

// logLine returns a line of the error log for people: its time as an age,
// then its message as a sentence.
func logLine(line string) string {
	_, message, hasStamp := strings.Cut(line, " ")
	moment, err := logTime(line)
	if !hasStamp || err != nil {
		return terminal.Printable(line)
	}
	return timeAgo(moment) + ": " + sentence(terminal.Printable(message))
}
