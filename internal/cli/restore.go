package cli

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
)

func undoCommand(parsed flags, env Env) error {
	projectStore, err := openStore(env, parsed)
	if isMissingProject(err) {
		printNoCheckpoints(env, parsed)
		return nil
	}
	if err != nil {
		return err
	}
	defer projectStore.Close()
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

func toCommand(parsed flags, env Env) error {
	if len(parsed.args) != 1 {
		return usageError{message: "undo to needs a checkpoint id; undo list shows them"}
	}
	projectStore, checkpoint, err := findCheckpoint(env, parsed)
	if err != nil {
		return err
	}
	defer projectStore.Close()
	return restoreTo(env, projectStore, checkpoint, parsed.isConfirmed)
}

func diffCommand(parsed flags, env Env) error {
	if len(parsed.args) != 1 {
		return usageError{message: "undo diff needs a checkpoint id; undo list shows them"}
	}
	projectStore, checkpoint, err := findCheckpoint(env, parsed)
	if err != nil {
		return err
	}
	defer projectStore.Close()
	changes, err := restore.Plan(projectStore, checkpoint.ID)
	if err != nil {
		return err
	}
	out := env.stdout()
	printCheckpointHeading(out, checkpoint, projectStore.Root())
	if changes.Len() == 0 {
		out.Success("The project already matches checkpoint %d.", checkpoint.ID)
		return nil
	}
	printChanges(out, changes)
	out.Println()
	out.Printf("Run %s to change these %s.\n", out.Bold(fmt.Sprintf("undo to %d", checkpoint.ID)), pluralize(changes.Len(), "path"))
	return nil
}

func findCheckpoint(env Env, parsed flags) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(parsed.args[0])
	if err != nil {
		return nil, store.Checkpoint{}, usageError{message: fmt.Sprintf("%q is not a checkpoint id; undo list shows them", parsed.args[0])}
	}
	projectStore, err := openStore(env, parsed)
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	checkpoint, err := projectStore.Checkpoint(id)
	if err != nil {
		projectStore.Close()
		return nil, store.Checkpoint{}, fmt.Errorf("%w (see undo list)", err)
	}
	return projectStore, checkpoint, nil
}

// restoreTo restores checkpoint. Without isConfirmed, it shows the changes
// and asks first. With it, it skips that preview, which costs a scan of the
// project, and shows the changes it made instead.
func restoreTo(env Env, projectStore *store.Store, checkpoint store.Checkpoint, isConfirmed bool) error {
	out := env.stdout()
	printCheckpointHeading(out, checkpoint, projectStore.Root())
	if !isConfirmed {
		changes, err := restore.Plan(projectStore, checkpoint.ID)
		if err != nil {
			return err
		}
		if changes.Len() == 0 {
			out.Success("Nothing to restore: the project already matches checkpoint %d.", checkpoint.ID)
			return nil
		}
		printChanges(out, changes)
		out.Println()
		if !confirm(env, out.Bold(fmt.Sprintf("Restore %s?", pluralize(changes.Len(), "path")))+" [y/N] ") {
			return errDeclined
		}
	}
	applied, err := restore.Apply(projectStore, checkpoint.ID, env.checkpointer())
	if err != nil {
		return err
	}
	if isConfirmed {
		printChanges(out, applied)
		out.Println()
	}
	if printNotRestored(out, applied) {
		out.Println()
	}
	restored := applied.Len() - len(applied.Skipped) - len(applied.Lost)
	out.Success("Restored %s. Run %s again to put them back.", pluralize(restored, "path"), out.Bold("undo"))
	return nil
}

// printCheckpointHeading names checkpoint, its age, and the command that
// followed it.
func printCheckpointHeading(out *terminal.Writer, checkpoint store.Checkpoint, root string) {
	out.Heading("Checkpoint %d · %s", checkpoint.ID, timeAgo(checkpoint.Time))
	if checkpoint.Label != "" {
		out.Printf("    %s %s\n", out.Dim("before"), terminal.Printable(checkpoint.Label))
	}
	if checkpoint.Dir != "" && checkpoint.Dir != root {
		out.Printf("    %s %s\n", out.Dim("in"), displayPath(checkpoint.Dir))
	}
	out.Println()
}

// changeRow is one line of a list of changes: what happens to a path, the
// path, and a detail such as how many paths it holds.
type changeRow struct {
	action string
	path   string
	detail string
}

// maxPathColumnWidth keeps one long path from pushing every detail far to
// the right.
const maxPathColumnWidth = 48

// printRows writes rows as aligned columns.
func printRows(out *terminal.Writer, rows []changeRow, colorAction func(string) string) {
	actionWidth, pathWidth := 0, 0
	for _, row := range rows {
		actionWidth = max(actionWidth, len(row.action))
		pathWidth = max(pathWidth, len(row.path))
	}
	pathWidth = min(pathWidth, maxPathColumnWidth)
	for _, row := range rows {
		line := fmt.Sprintf("  %s  %s", colorAction(fmt.Sprintf("%-*s", actionWidth, row.action)), row.path)
		if row.detail != "" {
			line += strings.Repeat(" ", max(pathWidth-len(row.path), 0)) + "  " + out.Dim(row.detail)
		}
		out.Println(line)
	}
}

// printNotRestored lists what the restore left alone and why, and reports
// whether it listed anything.
func printNotRestored(out *terminal.Writer, applied restore.Changes) bool {
	groups := []struct {
		action string
		paths  []string
		reason string
		color  func(string) string
	}{
		{"kept", applied.Kept, "unreadable, locked, or excluded since the checkpoint", out.Yellow},
		{"lost", applied.Lost, "its saved copy is gone, as after a power cut", out.Red},
		{"check", applied.Unstable, "it was changing while it was saved", out.Yellow},
		{"skipped", applied.Skipped, "sockets and devices cannot be restored", out.Dim},
	}
	hasRows := false
	for _, group := range groups {
		var rows []changeRow
		for _, rel := range group.paths {
			rows = append(rows, changeRow{action: group.action, path: terminal.Printable(rel), detail: group.reason})
		}
		if len(rows) > 0 {
			printRows(out, rows, group.color)
			hasRows = true
		}
	}
	return hasRows
}

func printChanges(out *terminal.Writer, changes restore.Changes) {
	printRows(out, groupedRows("restore", changes.Written), out.Green)
	printRows(out, groupedRows("remove", changes.Removed), out.Red)
}

// groupedRows returns one row for each top-level path. Everything inside a
// directory is shown as a single row with a count.
func groupedRows(action string, paths []string) []changeRow {
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
	rows := make([]changeRow, 0, len(order))
	for _, top := range order {
		members := groups[top]
		if len(members) == 1 {
			rows = append(rows, changeRow{action: action, path: terminal.Printable(members[0])})
			continue
		}
		rows = append(rows, changeRow{action: action, path: terminal.Printable(top) + "/", detail: pluralize(len(members), "path")})
	}
	return rows
}

func confirm(env Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
