package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

func statusCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	root, err := projectRoot(env, parsed)
	if err != nil {
		return err
	}
	matcher, err := exclude.New(root)
	if err != nil {
		return err
	}
	out := env.Stdout
	fmt.Fprintf(out, "Project      %s\n", printable(root))
	projectStore, err := store.Open(env.StoreHome, root)
	switch {
	case isMissingProject(err):
		fmt.Fprintln(out, "Checkpoints  none yet; the first agent command or undo checkpoint takes one")
	case err != nil:
		return err
	default:
		if err := printStore(out, env, projectStore); err != nil {
			return err
		}
	}
	if agents := installedAgentTitles(); len(agents) > 0 {
		fmt.Fprintf(out, "Hooks        %s\n", strings.Join(agents, ", "))
	} else {
		fmt.Fprintln(out, "Hooks        none yet, run: undo install <agent>")
	}
	fmt.Fprintf(out, "Excluded     %s\n", printable(strings.Join(matcher.Patterns(), ", ")))
	fmt.Fprintln(out, "Not covered  anything outside this folder: databases, git push, API calls")
	return nil
}

// printStore prints what the project's store holds and how it is kept.
func printStore(out io.Writer, env Env, projectStore *store.Store) error {
	diskUsage, err := projectStore.Usage()
	if err != nil {
		return err
	}
	watcher, err := env.watcher().Status(projectStore)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Store        %s\n", printable(projectStore.Dir()))
	if err := printCheckpoints(out, projectStore); err != nil {
		return err
	}
	if projectStore.IsCopyMode() {
		fmt.Fprintln(out, "Storage      full copies, because copy mode is on")
	} else {
		fmt.Fprintln(out, "Storage      clones that share data with the project")
	}
	fmt.Fprintf(out, "Disk used    %s not shared with the project, limit %s\n", formatBytes(diskUsage), formatBytes(store.DefaultLimits().MaxBytes))
	switch reason, isRecent := watch.StartFailure(projectStore); {
	case watcher.IsRunning:
		fmt.Fprintf(out, "Watcher      running, %d checkpoints since it started\n", watcher.CheckpointCount)
	case isRecent:
		fmt.Fprintf(out, "Watcher      could not start, so checkpoints scan the project: %s\n", printable(reason))
	default:
		fmt.Fprintln(out, "Watcher      not running, so checkpoints scan the project")
	}
	if line := projectStore.LastError(); line != "" {
		fmt.Fprintf(out, "Last error   %s\n", printable(line))
	}
	return nil
}

// printCheckpoints prints how many checkpoints there are, what the latest one
// could not save, and how many hold files that were changing while they were
// saved.
func printCheckpoints(out io.Writer, projectStore *store.Store) error {
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if len(checkpoints) == 0 {
		fmt.Fprintln(out, "Checkpoints  0")
		return nil
	}
	fmt.Fprintf(out, "Checkpoints  %d (oldest %s)\n", len(checkpoints), timeAgo(checkpoints[0].Time))
	var unstableCount int
	for _, checkpoint := range checkpoints {
		if manifest, err := projectStore.Manifest(checkpoint.ID); err == nil && len(manifest.Unstable) > 0 {
			unstableCount++
		}
	}
	latest, err := projectStore.Manifest(checkpoints[len(checkpoints)-1].ID)
	if err != nil {
		return err
	}
	if len(latest.Unreadable) > 0 {
		fmt.Fprintf(out, "Unreadable   %s (undo has no permission to read these, so they are not covered)\n", printable(strings.Join(latest.Unreadable, ", ")))
	}
	if unstableCount > 0 {
		fmt.Fprintf(out, "Changing     %d checkpoints hold files that changed while they were saved; a restore lists them\n", unstableCount)
	}
	return nil
}

func formatBytes(size int64) string {
	if size < 1000 {
		return fmt.Sprintf("%d B", size)
	}
	value, unit := float64(size)/1000, "kB"
	for _, larger := range []string{"MB", "GB"} {
		if value < 1000 {
			break
		}
		value, unit = value/1000, larger
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func timeAgo(moment time.Time) string {
	elapsed := time.Since(moment).Round(time.Second)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return moment.Format("Jan 2 15:04")
	}
}
