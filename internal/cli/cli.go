// Package cli is the undo command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

// Version is set when the binary is built.
var Version = "dev"

const usage = `undo puts your project back the way it was before an agent's command.

Usage:
  undo                     restore the state before the last command
  undo list                show checkpoints
  undo diff <id>           show what restoring checkpoint <id> would change
  undo to <id>             restore the state at checkpoint <id>
  undo run -- <command>    take a checkpoint, then run <command>
  undo checkpoint          take a checkpoint now
  undo status              show what undo protects
  undo install <agent>     checkpoint before every change the agent makes:
                           claude, codex, cursor, gemini or opencode
  undo uninstall <agent>   remove that hook
  undo version             print the version

Flags:
  --yes                    restore without asking
  --label <text>           label for undo checkpoint
  --copy                   keep full copies, for filesystems without clones
`

// Env holds the directory and streams a command works with.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// StartWatcher starts a background watcher for the project at root. When
	// it is nil, checkpoints scan the project instead.
	StartWatcher func(root string) error
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run runs the command in args and returns the exit code for the process.
func Run(args []string, env Env) int {
	err := dispatch(args, env)
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	if usageErr, ok := errors.AsType[usageError](err); ok {
		fmt.Fprintf(env.Stderr, "undo: %s\n\n%s", usageErr.msg, usage)
		return 2
	}
	switch {
	case errors.Is(err, errDeclined):
		return 1
	case errors.Is(err, store.ErrCloneUnsupported):
		fmt.Fprintf(env.Stderr, "undo: %s, so it cannot protect this project cheaply.\n"+
			"Run 'undo checkpoint --copy' once to keep full copies instead. They take as much disk as the project.\n", err)
		return 1
	default:
		fmt.Fprintf(env.Stderr, "undo: %s\n", err)
		return 1
	}
}

var errDeclined = errors.New("declined")

func dispatch(args []string, env Env) error {
	command, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, rest = args[0], args[1:]
	}
	switch command {
	case "":
		return undoCommand(rest, env)
	case "list":
		return listCommand(env)
	case "diff":
		return diffCommand(rest, env)
	case "to":
		return toCommand(rest, env)
	case "run":
		return runCommand(rest, env)
	case "checkpoint":
		return checkpointCommand(rest, env)
	case "status":
		return statusCommand(env)
	case "install":
		return installCommand(rest, env)
	case "uninstall":
		return uninstallCommand(rest, env)
	case "hook":
		checkpoint := func(s *store.Store, label, dir string) (store.Checkpoint, error) {
			return takeCheckpoint(s, label, dir, env)
		}
		// A hook must never fail, because some agents block the tool when it
		// does. An unknown agent name is handled like any other agent.
		var agent hook.Agent
		if len(rest) > 0 {
			agent, _ = hook.FindAgent(rest[0])
		}
		// Handle logs its failures in the store.
		_ = agent.Handle(env.Stdin, checkpoint)
		return nil
	case "watch":
		return watchCommand(env)
	case "version":
		fmt.Fprintln(env.Stdout, "undo", Version)
		return nil
	case "help":
		fmt.Fprint(env.Stdout, usage)
		return nil
	}
	return usageError{fmt.Sprintf("unknown command %q", command)}
}

func openStore(env Env) (*store.Store, error) {
	root, err := project.Root(env.Dir)
	if err != nil {
		return nil, err
	}
	return store.Open(root)
}

// flags holds the parsed --yes and --label flags and the other arguments.
type flags struct {
	yes   bool
	copy  bool
	label string
	args  []string
}

func parseFlags(args []string) (flags, error) {
	var f flags
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--yes" || arg == "-y":
			f.yes = true
		case arg == "--copy":
			f.copy = true
		case arg == "--label":
			if i+1 == len(args) {
				return f, usageError{"--label needs a value"}
			}
			i++
			f.label = args[i]
		case strings.HasPrefix(arg, "--label="):
			f.label = strings.TrimPrefix(arg, "--label=")
		case strings.HasPrefix(arg, "-"):
			return f, usageError{fmt.Sprintf("unknown flag %q", arg)}
		default:
			f.args = append(f.args, arg)
		}
	}
	return f, nil
}

func undoCommand(args []string, env Env) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	s, err := openStore(env)
	if err != nil {
		return err
	}
	cps, err := s.List()
	if err != nil {
		return err
	}
	if len(cps) == 0 {
		return fmt.Errorf("no checkpoints yet for %s", s.Root())
	}
	return restoreTo(s, cps[len(cps)-1], f.yes, env)
}

func toCommand(args []string, env Env) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return usageError{"to needs a checkpoint id"}
	}
	s, cp, err := findCheckpoint(f.args[0], env)
	if err != nil {
		return err
	}
	return restoreTo(s, cp, f.yes, env)
}

func findCheckpoint(arg string, env Env) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(arg)
	if err != nil {
		return nil, store.Checkpoint{}, usageError{fmt.Sprintf("%q is not a checkpoint id", arg)}
	}
	s, err := openStore(env)
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	cps, err := s.List()
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	for _, cp := range cps {
		if cp.ID == id {
			return s, cp, nil
		}
	}
	return nil, store.Checkpoint{}, fmt.Errorf("no checkpoint %d (see undo list)", id)
}

// restoreTo restores cp. Without yes, it shows the changes and asks first.
// With yes, it skips that preview, which costs a scan of the project, and shows
// the changes it made instead.
func restoreTo(s *store.Store, cp store.Checkpoint, yes bool, env Env) error {
	fmt.Fprintf(env.Stdout, "Checkpoint %d, %s, before: %s\n", cp.ID, timeAgo(cp.Time), cp.Label)
	if !yes {
		changes, err := restore.Plan(s, cp.ID)
		if err != nil {
			return err
		}
		if len(changes.Write)+len(changes.Remove) == 0 {
			fmt.Fprintln(env.Stdout, "Nothing to restore: the project already matches this checkpoint.")
			return nil
		}
		printChanges(env.Stdout, changes)
		if !confirm(env, fmt.Sprintf("Restore %d paths? [y/N] ", len(changes.Write)+len(changes.Remove))) {
			return errDeclined
		}
	}
	applied, err := restore.Apply(s, cp.ID, func(s *store.Store, label, dir string) (store.Checkpoint, error) {
		return takeCheckpoint(s, label, dir, env)
	})
	if err != nil {
		return err
	}
	if yes {
		printChanges(env.Stdout, applied)
	}
	fmt.Fprintf(env.Stdout, "Restored %d paths. Run undo again to put them back.\n", len(applied.Write)+len(applied.Remove))
	return nil
}

func printChanges(w io.Writer, changes restore.Changes) {
	printGrouped(w, "restore", changes.Write)
	printGrouped(w, "delete ", changes.Remove)
}

// printGrouped prints one line for each top-level path. Everything inside a
// directory is shown as a single line with a count.
func printGrouped(w io.Writer, verb string, paths []string) {
	var order []string
	groups := map[string][]string{}
	for _, rel := range paths {
		if rel == "." {
			continue
		}
		top, _, _ := strings.Cut(rel, "/")
		if _, ok := groups[top]; !ok {
			order = append(order, top)
		}
		groups[top] = append(groups[top], rel)
	}
	for _, top := range order {
		members := groups[top]
		if len(members) == 1 {
			fmt.Fprintf(w, "  %s  %s\n", verb, members[0])
			continue
		}
		fmt.Fprintf(w, "  %s  %s/  (%d paths)\n", verb, top, len(members))
	}
}

func confirm(env Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func diffCommand(args []string, env Env) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return usageError{"diff needs a checkpoint id"}
	}
	s, cp, err := findCheckpoint(f.args[0], env)
	if err != nil {
		return err
	}
	changes, err := restore.Plan(s, cp.ID)
	if err != nil {
		return err
	}
	if len(changes.Write)+len(changes.Remove) == 0 {
		fmt.Fprintln(env.Stdout, "No changes since this checkpoint.")
		return nil
	}
	printChanges(env.Stdout, changes)
	return nil
}

func listCommand(env Env) error {
	s, err := openStore(env)
	if err != nil {
		return err
	}
	cps, err := s.List()
	if err != nil {
		return err
	}
	if len(cps) == 0 {
		fmt.Fprintf(env.Stdout, "No checkpoints yet for %s\n", s.Root())
		return nil
	}
	for _, cp := range cps {
		label := cp.Label
		if cp.Dir != "" && cp.Dir != s.Root() {
			label += "  (in " + cp.Dir + ")"
		}
		fmt.Fprintf(env.Stdout, "%5d  %-9s  %s\n", cp.ID, timeAgo(cp.Time), label)
	}
	return nil
}

func checkpointCommand(args []string, env Env) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	s, err := openStore(env)
	if err != nil {
		return err
	}
	if f.copy {
		if err := s.SetCopyMode(true); err != nil {
			return err
		}
	}
	cp, err := takeCheckpoint(s, f.label, env.Dir, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Checkpoint %d\n", cp.ID)
	return nil
}

func runCommand(args []string, env Env) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usageError{"run needs a command"}
	}
	s, err := openStore(env)
	if err != nil {
		return err
	}
	if _, err := takeCheckpoint(s, strings.Join(args, " "), env.Dir, env); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}

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

func installCommand(args []string, env Env) error {
	agent, err := agentArg("install", args)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	path, err := agent.Install(binary)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Updated %s\n%s\n", path, agent.Note)
	return nil
}

func uninstallCommand(args []string, env Env) error {
	agent, err := agentArg("uninstall", args)
	if err != nil {
		return err
	}
	path, err := agent.Uninstall()
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Removed the undo hook from %s\n", path)
	return nil
}

func agentArg(command string, args []string) (hook.Agent, error) {
	if len(args) == 1 {
		if agent, ok := hook.FindAgent(args[0]); ok {
			return agent, nil
		}
	}
	return hook.Agent{}, usageError{fmt.Sprintf("%s supports: %s", command, strings.Join(agentNames(), ", "))}
}

func agentNames() []string {
	var names []string
	for _, agent := range hook.Agents {
		names = append(names, agent.Name)
	}
	return names
}

// takeCheckpoint checkpoints through the project's watcher when one can be started,
// and by scanning the project otherwise.
func takeCheckpoint(s *store.Store, label, dir string, env Env) (store.Checkpoint, error) {
	if env.StartWatcher == nil {
		return s.Take(label, dir)
	}
	return watch.Checkpoint(s, label, dir, func() error { return env.StartWatcher(s.Root()) })
}

func watchCommand(env Env) error {
	s, err := openStore(env)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := watch.Serve(ctx, s, watch.Options{Idle: time.Hour, Limits: store.DefaultLimits}); err != nil && !errors.Is(err, watch.ErrRunning) {
		s.LogError(fmt.Errorf("watcher stopped: %w", err))
		return err
	}
	return nil
}
