package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
)

func undoCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return err
	}
	latest, hasLatest, err := projectStore.Latest()
	if err != nil {
		return err
	}
	if !hasLatest {
		printNoCheckpoints(env, projectStore)
		return nil
	}
	return restoreTo(projectStore, latest, parsed.isConfirmed, env)
}

func toCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(parsed.args) != 1 {
		return usageError{"to needs a checkpoint id"}
	}
	projectStore, cp, err := findCheckpoint(parsed, env)
	if err != nil {
		return err
	}
	return restoreTo(projectStore, cp, parsed.isConfirmed, env)
}

func diffCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(parsed.args) != 1 {
		return usageError{"diff needs a checkpoint id"}
	}
	projectStore, cp, err := findCheckpoint(parsed, env)
	if err != nil {
		return err
	}
	changes, err := restore.Plan(projectStore, cp.ID)
	if err != nil {
		return err
	}
	if changes.Len() == 0 {
		fmt.Fprintln(env.Stdout, "No changes since this checkpoint.")
		return nil
	}
	printChanges(env.Stdout, changes)
	return nil
}

func listCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return err
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if len(checkpoints) == 0 {
		printNoCheckpoints(env, projectStore)
		return nil
	}
	for _, cp := range checkpoints {
		label := printable(cp.Label)
		if cp.Dir != "" && cp.Dir != projectStore.Root() {
			label += "  (in " + printable(cp.Dir) + ")"
		}
		fmt.Fprintf(env.Stdout, "%5d  %-9s  %s\n", cp.ID, timeAgo(cp.Time), label)
	}
	return nil
}

// printNoCheckpoints says the project has no checkpoints, and points to any
// deleted project inside it that does, since running undo in the parent of a
// deleted folder is how people look for it.
func printNoCheckpoints(env Env, projectStore *store.Store) {
	fmt.Fprintf(env.Stdout, "No checkpoints yet for %s\n", printable(projectStore.Root()))
	stores, err := store.Stores()
	if err != nil {
		return
	}
	for _, other := range stores {
		root := other.Root()
		if filepath.Dir(root) != projectStore.Root() {
			continue
		}
		if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if latest, hasLatest, err := other.Latest(); err == nil && hasLatest {
			fmt.Fprintf(env.Stdout, "%s was deleted. Bring it back with: undo to %d --project %s\n",
				printable(root), latest.ID, strconv.Quote(root))
		}
	}
}

func findCheckpoint(parsed flags, env Env) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(parsed.args[0])
	if err != nil {
		return nil, store.Checkpoint{}, usageError{fmt.Sprintf("%q is not a checkpoint id", parsed.args[0])}
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	cp, err := projectStore.Checkpoint(id)
	if err != nil {
		return nil, store.Checkpoint{}, fmt.Errorf("%w (see undo list)", err)
	}
	return projectStore, cp, nil
}

// restoreTo restores cp. Without isConfirmed, it shows the changes and asks
// first. With it, it skips that preview, which costs a scan of the project,
// and shows the changes it made instead.
func restoreTo(projectStore *store.Store, cp store.Checkpoint, isConfirmed bool, env Env) error {
	fmt.Fprintf(env.Stdout, "Checkpoint %d, %s, before: %s\n", cp.ID, timeAgo(cp.Time), printable(cp.Label))
	if !isConfirmed {
		changes, err := restore.Plan(projectStore, cp.ID)
		if err != nil {
			return err
		}
		if changes.Len() == 0 {
			fmt.Fprintln(env.Stdout, "Nothing to restore: the project already matches this checkpoint.")
			return nil
		}
		printChanges(env.Stdout, changes)
		if !confirm(env, fmt.Sprintf("Restore %d paths? [y/N] ", changes.Len())) {
			return errDeclined
		}
	}
	applied, err := restore.Apply(projectStore, cp.ID, env.checkpointer())
	if err != nil {
		return err
	}
	if isConfirmed {
		printChanges(env.Stdout, applied)
	}
	for _, rel := range applied.Skipped {
		fmt.Fprintf(env.Stdout, "  skipped  %s  (sockets and devices cannot be restored)\n", printable(rel))
	}
	fmt.Fprintf(env.Stdout, "Restored %d paths. Run undo again to put them back.\n", applied.Len())
	return nil
}

func printChanges(writer io.Writer, changes restore.Changes) {
	printGrouped(writer, "restore", changes.Written)
	printGrouped(writer, "delete ", changes.Removed)
}

// printGrouped prints one line for each top-level path. Everything inside a
// directory is shown as a single line with a count.
func printGrouped(writer io.Writer, verb string, paths []string) {
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
			fmt.Fprintf(writer, "  %s  %s\n", verb, printable(members[0]))
			continue
		}
		fmt.Fprintf(writer, "  %s  %s/  (%d paths)\n", verb, printable(top), len(members))
	}
}

func confirm(env Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
