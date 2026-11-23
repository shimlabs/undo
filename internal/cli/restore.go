package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/shell"
	"github.com/shimlabs/undo/internal/store"
)

func undoCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	projectStore, err := openStore(env, parsed)
	if isMissingProject(err) {
		printNoCheckpoints(env, parsed)
		return nil
	}
	if err != nil {
		return err
	}
	latest, hasLatest, err := projectStore.Latest()
	if err != nil {
		return err
	}
	if !hasLatest {
		printNoCheckpoints(env, parsed)
		return nil
	}
	return restoreTo(env, projectStore, latest, parsed.isConfirmed)
}

func toCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(parsed.args) != 1 {
		return usageError{"to needs a checkpoint id"}
	}
	projectStore, checkpoint, err := findCheckpoint(env, parsed)
	if err != nil {
		return err
	}
	return restoreTo(env, projectStore, checkpoint, parsed.isConfirmed)
}

func diffCommand(args []string, env Env) error {
	parsed, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(parsed.args) != 1 {
		return usageError{"diff needs a checkpoint id"}
	}
	projectStore, checkpoint, err := findCheckpoint(env, parsed)
	if err != nil {
		return err
	}
	changes, err := restore.Plan(projectStore, checkpoint.ID)
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
	if isMissingProject(err) {
		printNoCheckpoints(env, parsed)
		return nil
	}
	if err != nil {
		return err
	}
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if len(checkpoints) == 0 {
		printNoCheckpoints(env, parsed)
		return nil
	}
	for _, checkpoint := range checkpoints {
		label := printable(checkpoint.Label)
		if checkpoint.Dir != "" && checkpoint.Dir != projectStore.Root() {
			label += "  (in " + printable(checkpoint.Dir) + ")"
		}
		fmt.Fprintf(env.Stdout, "%5d  %-9s  %s\n", checkpoint.ID, timeAgo(checkpoint.Time), label)
	}
	return nil
}

// isMissingProject reports whether err means there is nothing to restore
// here: no store yet, or a folder such as the home directory that is not a
// project but may hold deleted ones.
func isMissingProject(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, project.ErrNotProject)
}

// printNoCheckpoints says there are no checkpoints for the project, and points
// to any deleted project directly inside the folder that has some, since
// running undo in the parent of a deleted folder is how people look for it.
func printNoCheckpoints(env Env, parsed flags) {
	dir := env.Dir
	if root, err := projectRoot(env, parsed); err == nil {
		dir = root
	}
	fmt.Fprintf(env.Stdout, "No checkpoints yet for %s\n", printable(dir))
	stores, err := store.StoresUnder(env.StoreHome, env.Dir)
	if err != nil {
		return
	}
	for _, other := range stores {
		root := other.Root()
		if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if latest, hasLatest, err := other.Latest(); err == nil && hasLatest {
			if shown := printable(root); shown != root {
				// No quoting both shows such a path and stays safe to paste.
				fmt.Fprintf(env.Stdout, "%s was deleted. Bring it back with undo to %d and --project set to that path.\n", shown, latest.ID)
			} else {
				fmt.Fprintf(env.Stdout, "%s was deleted. Bring it back with: undo to %d --project %s\n", root, latest.ID, shell.Quote(root))
			}
		}
	}
}

func findCheckpoint(env Env, parsed flags) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(parsed.args[0])
	if err != nil {
		return nil, store.Checkpoint{}, usageError{fmt.Sprintf("%q is not a checkpoint id", parsed.args[0])}
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	checkpoint, err := projectStore.Checkpoint(id)
	if err != nil {
		return nil, store.Checkpoint{}, fmt.Errorf("%w (see undo list)", err)
	}
	return projectStore, checkpoint, nil
}

// restoreTo restores checkpoint. Without isConfirmed, it shows the changes
// and asks first. With it, it skips that preview, which costs a scan of the
// project, and shows the changes it made instead.
func restoreTo(env Env, projectStore *store.Store, checkpoint store.Checkpoint, isConfirmed bool) error {
	fmt.Fprintf(env.Stdout, "Checkpoint %d, %s, before: %s\n", checkpoint.ID, timeAgo(checkpoint.Time), printable(checkpoint.Label))
	if !isConfirmed {
		changes, err := restore.Plan(projectStore, checkpoint.ID)
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
	applied, err := restore.Apply(projectStore, checkpoint.ID, env.checkpointer())
	if err != nil {
		return err
	}
	if isConfirmed {
		printChanges(env.Stdout, applied)
	}
	printNotRestored(env.Stdout, applied)
	fmt.Fprintf(env.Stdout, "Restored %d paths. Run undo again to put them back.\n", applied.Len())
	return nil
}

// printNotRestored lists what the restore left alone and why.
func printNotRestored(out io.Writer, applied restore.Changes) {
	for _, rel := range applied.Skipped {
		fmt.Fprintf(out, "  skipped  %s  (sockets and devices cannot be restored)\n", printable(rel))
	}
	for _, rel := range applied.Kept {
		fmt.Fprintf(out, "  kept     %s  (never saved: unreadable, or excluded since the checkpoint)\n", printable(rel))
	}
	for _, rel := range applied.Unstable {
		fmt.Fprintf(out, "  check    %s  (it was changing while it was saved)\n", printable(rel))
	}
}

func printChanges(out io.Writer, changes restore.Changes) {
	printGrouped(out, "restore", changes.Written)
	printGrouped(out, "delete ", changes.Removed)
}

// printGrouped prints one line for each top-level path. Everything inside a
// directory is shown as a single line with a count.
func printGrouped(out io.Writer, verb string, paths []string) {
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
			fmt.Fprintf(out, "  %s  %s\n", verb, printable(members[0]))
			continue
		}
		fmt.Fprintf(out, "  %s  %s/  (%d paths)\n", verb, printable(top), len(members))
	}
}

func confirm(env Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
