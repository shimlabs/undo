package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/shell"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
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
	defer projectStore.Close()
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
	defer projectStore.Close()
	checkpoints, err := projectStore.List()
	if err != nil {
		return err
	}
	if len(checkpoints) == 0 {
		printNoCheckpoints(env, parsed)
		return nil
	}
	printCheckpoints(env.stdout(), checkpoints, projectStore.Root())
	return nil
}

// printCheckpoints lists checkpoints in a table: id, age, the command that
// followed, and where and by which agent it ran when that adds something.
func printCheckpoints(out *terminal.Writer, checkpoints []store.Checkpoint, root string) {
	out.Heading("Checkpoints in %s", displayPath(root))
	idWidth := len(strconv.Itoa(checkpoints[len(checkpoints)-1].ID))
	ages := make([]string, len(checkpoints))
	ageWidth := 0
	for i, checkpoint := range checkpoints {
		ages[i] = timeAgo(checkpoint.Time)
		ageWidth = max(ageWidth, len(ages[i]))
	}
	for i, checkpoint := range checkpoints {
		var details []string
		if checkpoint.Dir != "" && checkpoint.Dir != root {
			details = append(details, "in "+displayPath(checkpoint.Dir))
		}
		if checkpoint.Agent != "" {
			details = append(details, checkpoint.Agent)
		}
		line := fmt.Sprintf("  %s  %s  %s", out.Bold(fmt.Sprintf("%*d", idWidth, checkpoint.ID)), out.Dim(fmt.Sprintf("%-*s", ageWidth, ages[i])), terminal.Printable(checkpoint.Label))
		if len(details) > 0 {
			line += "  " + out.Cyan(strings.Join(details, " · "))
		}
		out.Println(line)
	}
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
	out := env.stdout()
	out.Printf("No checkpoints yet in %s. The next agent command, or %s, takes one.\n", displayPath(dir), out.Bold("undo checkpoint"))
	stores, err := store.StoresUnder(env.StoreHome, env.Dir)
	if err != nil {
		return
	}
	for _, other := range stores {
		printDeletedProject(out, other)
		other.Close()
	}
}

// printDeletedProject says how to bring back the project of projectStore if
// its folder was deleted.
func printDeletedProject(out *terminal.Writer, projectStore *store.Store) {
	root := projectStore.Root()
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	latest, hasLatest, err := projectStore.Latest()
	if err != nil || !hasLatest {
		return
	}
	if shown := terminal.Printable(root); shown != root {
		// A path with control characters cannot be shown both readably
		// and safe to paste, so the command is described instead.
		out.Warning("%s was deleted. Bring it back with undo to %d and --project set to that path.", shown, latest.ID)
		return
	}
	out.Warning("%s was deleted. Bring it back with:", displayPath(root))
	out.Printf("  %s\n", out.Bold(fmt.Sprintf("undo to %d --project %s", latest.ID, shell.Quote(root))))
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
	out.Success("Restored %s. Run %s again to put them back.", pluralize(applied.Len(), "path"), out.Bold("undo"))
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
