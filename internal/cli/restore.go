package cli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
)

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
