package cli

import (
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

func statusCommand(parsed flags, env Env) error {
	root, err := projectRoot(env, parsed)
	if err != nil {
		return err
	}
	matcher, err := exclude.New(root)
	if err != nil {
		return err
	}
	out := env.stdout()
	out.Heading("%s", displayPath(root))
	projectStore, err := store.Open(env.StoreHome, root)
	switch {
	case isMissingProject(err):
		printField(out, "Checkpoints", "none yet; the next agent command, or "+out.Bold("undo checkpoint")+", takes one")
	case err != nil:
		return err
	default:
		defer projectStore.Close()
		if err := printStore(out, env, projectStore); err != nil {
			return err
		}
	}
	if agents := installedAgentTitles(); len(agents) > 0 {
		printField(out, "Hooks", strings.Join(agents, ", "))
	} else {
		printField(out, "Hooks", out.Yellow("none yet")+", run: "+out.Bold("undo install <agent>"))
	}
	printField(out, "Excluded", terminal.Printable(strings.Join(matcher.Patterns(), ", ")))
	printField(out, "Not covered", "anything outside this folder: databases, git push, API calls")
	return nil
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
	if err := printCheckpointSummary(out, projectStore); err != nil {
		return err
	}
	if projectStore.IsCopyMode() {
		printField(out, "Storage", "full copies, because copy mode is on")
	} else {
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
	if line := projectStore.LastError(); line != "" {
		printField(out, "Last error", out.Red(terminal.Printable(line)))
	}
	return nil
}

// printCheckpointSummary prints how many checkpoints there are, what the
// latest one could not save, and how many hold files that were changing while
// they were saved.
func printCheckpointSummary(out *terminal.Writer, projectStore *store.Store) error {
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if len(checkpoints) == 0 {
		printField(out, "Checkpoints", "0")
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
		printField(out, "Unreadable", out.Yellow(terminal.Printable(strings.Join(latest.Unreadable, ", ")))+" (undo has no permission to read these, so they are not covered)")
	}
	if unstableCount > 0 {
		printField(out, "Changing", out.Yellow(pluralize(unstableCount, "checkpoint"))+" hold files that changed while they were saved; a restore lists them")
	}
	return nil
}
