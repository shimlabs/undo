// Package cli is the undo command line.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
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
  undo version             print the version

Flags:
  --yes                    restore without asking
  --label <text>           label for undo checkpoint
`

// Env holds the directory and streams a command works with.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run runs the command in args and returns the exit code for the process.
func Run(args []string, env Env) int {
	err := dispatch(args, env)
	var exit *exec.ExitError
	var usageErr usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	case errors.As(err, &usageErr):
		fmt.Fprintf(env.Stderr, "undo: %s\n\n%s", usageErr.msg, usage)
		return 2
	case errors.Is(err, errDeclined):
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
		return undoLast(rest, env)
	case "list":
		return list(env)
	case "diff":
		return diff(rest, env)
	case "to":
		return to(rest, env)
	case "run":
		return runCommand(rest, env)
	case "checkpoint":
		return checkpoint(rest, env)
	case "status":
		return status(env)
	case "version":
		fmt.Fprintln(env.Stdout, "undo", Version)
		return nil
	case "help":
		fmt.Fprint(env.Stdout, usage)
		return nil
	}
	return usageError{fmt.Sprintf("unknown command %q", command)}
}

func open(env Env) (*store.Store, error) {
	root, err := project.Root(env.Dir)
	if err != nil {
		return nil, err
	}
	return store.Open(root)
}

// flags holds the parsed --yes and --label flags and the other arguments.
type flags struct {
	yes   bool
	label string
	args  []string
}

func parse(args []string) (flags, error) {
	var f flags
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--yes" || arg == "-y":
			f.yes = true
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

func undoLast(args []string, env Env) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	s, err := open(env)
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

func to(args []string, env Env) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return usageError{"to needs a checkpoint id"}
	}
	s, cp, err := find(f.args[0], env)
	if err != nil {
		return err
	}
	return restoreTo(s, cp, f.yes, env)
}

func find(arg string, env Env) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(arg)
	if err != nil {
		return nil, store.Checkpoint{}, usageError{fmt.Sprintf("%q is not a checkpoint id", arg)}
	}
	s, err := open(env)
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

func restoreTo(s *store.Store, cp store.Checkpoint, yes bool, env Env) error {
	changes, err := restore.Plan(s, cp.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Checkpoint %d, %s, before: %s\n", cp.ID, ago(cp.Time), cp.Label)
	if len(changes.Write)+len(changes.Remove) == 0 {
		fmt.Fprintln(env.Stdout, "Nothing to restore: the project already matches this checkpoint.")
		return nil
	}
	printChanges(env.Stdout, changes)
	if !yes && !confirm(env, fmt.Sprintf("Restore %d paths? [y/N] ", len(changes.Write)+len(changes.Remove))) {
		return errDeclined
	}
	applied, err := restore.Apply(s, cp.ID)
	if err != nil {
		return err
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

func diff(args []string, env Env) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return usageError{"diff needs a checkpoint id"}
	}
	s, cp, err := find(f.args[0], env)
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

func list(env Env) error {
	s, err := open(env)
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
		fmt.Fprintf(env.Stdout, "%5d  %-9s  %s\n", cp.ID, ago(cp.Time), label)
	}
	return nil
}

func checkpoint(args []string, env Env) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	s, err := open(env)
	if err != nil {
		return err
	}
	cp, err := s.Take(f.label, env.Dir)
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
	s, err := open(env)
	if err != nil {
		return err
	}
	if _, err := s.Take(strings.Join(args, " "), env.Dir); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = env.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	return cmd.Run()
}

func status(env Env) error {
	s, err := open(env)
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
	fmt.Fprintf(env.Stdout, "Project      %s\n", s.Root())
	fmt.Fprintf(env.Stdout, "Store        %s\n", s.Dir())
	fmt.Fprintf(env.Stdout, "Checkpoints  %d\n", len(cps))
	fmt.Fprintf(env.Stdout, "Excluded     %s\n", strings.Join(ex.Patterns(), ", "))
	fmt.Fprintln(env.Stdout, "Not covered  anything outside this folder: databases, git push, API calls")
	return nil
}

func ago(t time.Time) string {
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
