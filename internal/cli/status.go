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
	fmt.Fprintf(out, "Excluded     %s\n", strings.Join(matcher.Patterns(), ", "))
	fmt.Fprintln(out, "Not covered  anything outside this folder: databases, git push, API calls")
	return nil
}

// printStore prints what the project's store holds and how it is kept.
func printStore(out io.Writer, env Env, projectStore *store.Store) error {
	diskUsage, err := projectStore.Usage()
	if err != nil {
		return err
	}
	watcher, err := watch.Status(projectStore)
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
	fmt.Fprintf(out, "Disk used    %s not shared with the project, limit %s\n", formatBytes(diskUsage), formatBytes(store.DefaultLimits.MaxBytes))
	if watcher.IsRunning {
		fmt.Fprintf(out, "Watcher      running, %d checkpoints since it started\n", watcher.CheckpointCount)
	} else {
		fmt.Fprintln(out, "Watcher      not running, so checkpoints scan the project")
	}
	if line := projectStore.LastError(); line != "" {
		fmt.Fprintf(out, "Last error   %s\n", printable(line))
	}
	return nil
}

// printCheckpoints prints how many checkpoints there are and what the latest
// one could not save.
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
	manifest, err := projectStore.Manifest(checkpoints[len(checkpoints)-1].ID)
	if err != nil {
		return err
	}
	if len(manifest.Unreadable) > 0 {
		fmt.Fprintf(out, "Unreadable   %s (undo has no permission to read these, so they are not covered)\n", printable(strings.Join(manifest.Unreadable, ", ")))
	}
	if len(manifest.Unstable) > 0 {
		fmt.Fprintf(out, "Changing     %s (kept changing while they were saved, so the saved copy may be mid-write)\n", printable(strings.Join(manifest.Unstable, ", ")))
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
