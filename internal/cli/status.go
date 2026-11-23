package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

func statusCommand(env Env) error {
	s, err := openStore(env)
	if err != nil {
		return err
	}
	cps, err := s.List()
	if err != nil {
		return err
	}
	ex, err := exclude.New(s.Root())
	if err != nil {
		return err
	}
	usage, err := s.Usage()
	if err != nil {
		return err
	}
	watcher, err := watch.Status(s)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Project      %s\n", s.Root())
	fmt.Fprintf(env.Stdout, "Store        %s\n", s.Dir())
	if len(cps) == 0 {
		fmt.Fprintln(env.Stdout, "Checkpoints  0")
	} else {
		fmt.Fprintf(env.Stdout, "Checkpoints  %d (oldest %s)\n", len(cps), timeAgo(cps[0].Time))
	}
	if s.CopyMode() {
		fmt.Fprintln(env.Stdout, "Storage      full copies, because copy mode is on")
	} else {
		fmt.Fprintln(env.Stdout, "Storage      clones that share data with the project")
	}
	fmt.Fprintf(env.Stdout, "Disk used    %s not shared with the project, limit %s\n", formatBytes(usage), formatBytes(store.DefaultLimits.MaxBytes))
	if watcher.Running {
		fmt.Fprintln(env.Stdout, "Watcher      running")
	} else {
		fmt.Fprintln(env.Stdout, "Watcher      not running, so checkpoints scan the project")
	}
	var hooks []string
	for _, agent := range hook.Agents {
		if agent.IsInstalled() {
			hooks = append(hooks, agent.Title)
		}
	}
	if len(hooks) == 0 {
		fmt.Fprintln(env.Stdout, "Hooks        none yet, run: undo install <agent>")
	} else {
		fmt.Fprintf(env.Stdout, "Hooks        %s\n", strings.Join(hooks, ", "))
	}
	fmt.Fprintf(env.Stdout, "Excluded     %s\n", strings.Join(ex.Patterns(), ", "))
	fmt.Fprintln(env.Stdout, "Not covered  anything outside this folder: databases, git push, API calls")
	if line := s.LastError(); line != "" {
		fmt.Fprintf(env.Stdout, "Last error   %s\n", line)
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

func timeAgo(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2 15:04")
	}
}
