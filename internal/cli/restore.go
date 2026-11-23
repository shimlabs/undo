package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/shell"
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
	return restoreTo(env, parsed, projectStore, latest, true)
}

func toCommand(parsed flags, env Env) error {
	if len(parsed.args) != 1 {
		return usageError{message: "undo to needs a checkpoint id, which undo list shows"}
	}
	projectStore, checkpoint, err := findCheckpoint(env, parsed)
	if err != nil {
		return err
	}
	defer projectStore.Close()
	return restoreTo(env, parsed, projectStore, checkpoint, false)
}

func diffCommand(parsed flags, env Env) error {
	if len(parsed.args) != 1 {
		return usageError{message: "undo diff needs a checkpoint id, which undo list shows"}
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
	count := printChanges(out, changes)
	if count == 0 {
		out.Successf("The project already matches checkpoint %d.", checkpoint.ID)
		return nil
	}
	out.Println()
	out.Printf("Run %s to restore these %s.\n", out.Bold(projectCommand(parsed, "undo to "+strconv.Itoa(checkpoint.ID))), pluralize(count, "path"))
	return nil
}

// findCheckpoint opens the project's store and finds the checkpoint the
// first argument names. A project with no checkpoints gets the same
// explanation as undo list gives.
func findCheckpoint(env Env, parsed flags) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(parsed.args[0])
	if err != nil || id < 1 {
		return nil, store.Checkpoint{}, usageError{message: fmt.Sprintf("%q is not a checkpoint id, which undo list shows", parsed.args[0])}
	}
	projectStore, err := openStore(env, parsed)
	if isMissingProject(err) {
		printNoCheckpoints(env, parsed)
		return nil, store.Checkpoint{}, errAlreadyReported
	}
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	checkpoint, err := projectStore.Checkpoint(id)
	if err != nil {
		projectStore.Close()
		if errors.Is(err, store.ErrNoCheckpoint) {
			err = withHint(err, fmt.Sprintf("Run %s to see the checkpoints.", env.stderr().Bold(projectCommand(parsed, "undo list"))))
		}
		return nil, store.Checkpoint{}, err
	}
	return projectStore, checkpoint, nil
}

// restoreTo shows what restoring checkpoint changes and, unless --yes was
// given, asks first. isLatest says the user ran plain undo, which a project
// that already matches the latest checkpoint leaves with a pointer to the
// earlier ones.
func restoreTo(env Env, parsed flags, projectStore *store.Store, checkpoint store.Checkpoint, isLatest bool) error {
	out := env.stdout()
	if !parsed.isConfirmed && !isTerminal(env.Stdin) {
		command := "undo"
		if !isLatest {
			command = "undo to " + strconv.Itoa(checkpoint.ID)
		}
		return withHint(errNeedsTerminal,
			fmt.Sprintf("Run %s to restore without asking.", env.stderr().Bold(projectCommand(parsed, command+" --yes"))))
	}
	printCheckpointHeading(out, checkpoint, projectStore.Root())
	changes, err := restore.Plan(projectStore, checkpoint.ID)
	if err != nil {
		return err
	}
	count := printChanges(out, changes)
	if count == 0 {
		out.Successf("Nothing to restore: the project already matches checkpoint %d.", checkpoint.ID)
		if isLatest {
			out.Printf("Run %s to pick an earlier checkpoint, then %s.\n", out.Bold(projectCommand(parsed, "undo list")), out.Bold("undo to <id>"))
		}
		return nil
	}
	out.Println()
	if !parsed.isConfirmed && !confirm(env, out.Bold(fmt.Sprintf("Restore %s?", pluralize(count, "path")))+" [y/N] ") {
		out.Println("Nothing restored.")
		return errDeclined
	}
	applied, err := restore.Apply(projectStore, checkpoint.ID, env.checkpointer())
	if err != nil {
		return err
	}
	if printNotRestored(out, applied) {
		out.Println()
	}
	out.Successf("Restored %s. Run %s to put them back.", pluralize(restoredCount(applied), "path"), out.Bold(projectCommand(parsed, "undo")))
	return nil
}

// projectCommand returns command with the --project flag the user gave, so
// a suggested command acts on the same project.
func projectCommand(parsed flags, command string) string {
	if parsed.project == "" {
		return command
	}
	return command + " --project " + shell.Join([]string{parsed.project})
}

// isTerminal reports whether in is a terminal a person can answer from.
// Input that is not a file, as in tests, counts as one.
func isTerminal(in any) bool {
	file, isFile := in.(*os.File)
	return !isFile || term.IsTerminal(int(file.Fd()))
}

// printCheckpointHeading names checkpoint, its age, and the command that
// followed it.
func printCheckpointHeading(out *terminal.Writer, checkpoint store.Checkpoint, root string) {
	out.Headingf("Checkpoint %d · %s", checkpoint.ID, timeAgo(checkpoint.Time))
	if label := displayLabel(checkpoint.Label); label != "" {
		out.Printf("    %s %s\n", out.Dim("before"), label)
	}
	if checkpoint.Dir != "" && checkpoint.Dir != root {
		out.Printf("    %s %s\n", out.Dim("in"), displayPath(checkpoint.Dir))
	}
	out.Println()
}

// displayLabel returns label safe to print, without a leading "before",
// which the heading already says.
func displayLabel(label string) string {
	return terminal.Printable(strings.TrimPrefix(label, "before "))
}

// changeRow is one line of a list of changes: what happens to a path, the
// path, and a detail such as how many paths it holds. count is how many
// changes the row stands for.
type changeRow struct {
	action string
	path   string
	detail string
	count  int
	// color colors the action.
	color func(string) string
}

// maxPathColumnWidth keeps one long path from pushing every detail far to
// the right.
const maxPathColumnWidth = 48

// printRows writes rows as aligned columns.
func printRows(out *terminal.Writer, rows []changeRow) {
	actionWidth, pathWidth := 0, 0
	for _, row := range rows {
		actionWidth = max(actionWidth, len(row.action))
		pathWidth = max(pathWidth, len(row.path))
	}
	pathWidth = min(pathWidth, maxPathColumnWidth)
	for _, row := range rows {
		line := fmt.Sprintf("  %s  %s", row.color(fmt.Sprintf("%-*s", actionWidth, row.action)), row.path)
		if row.detail != "" {
			line += strings.Repeat(" ", max(pathWidth-len(row.path), 0)) + "  " + out.Dim(row.detail)
		}
		out.Println(line)
	}
}

// keepReasons says, for each reason a restore keeps a path, what happened
// and what to do about it.
func keepReasons() map[restore.KeepReason]string {
	return map[restore.KeepReason]string{
		restore.KeptUnreadable: "undo cannot read it; fix its permissions and run undo again",
		restore.KeptLocked:     "it or its folder is locked; unlock it and run undo again",
		restore.KeptExcluded:   "excluded since the checkpoint; remove the rule from " + ".undoignore to restore it",
		restore.KeptInTheWay:   "it holds files undo leaves alone, such as excluded ones",
	}
}

// printNotRestored lists what the restore left alone and why, and reports
// whether it listed anything.
func printNotRestored(out *terminal.Writer, applied restore.Changes) bool {
	rows := make([]changeRow, 0, len(applied.Kept)+len(applied.Lost)+len(applied.Unstable)+len(applied.Skipped))
	reasons := keepReasons()
	for _, rel := range applied.Kept {
		rows = append(rows, changeRow{action: "kept", path: displayRel(rel, false), detail: reasons[applied.KeepReasons[rel]], color: out.Yellow})
	}
	rows = append(rows, reasonRows("lost", applied.Lost, "its saved copy is gone, as after a power cut", out.Red)...)
	rows = append(rows, reasonRows("check", applied.Unstable, "it was changing while it was saved", out.Yellow)...)
	rows = append(rows, reasonRows("skipped", applied.Skipped, "sockets and devices cannot be restored", out.Dim)...)
	printRows(out, rows)
	return len(rows) > 0
}

func reasonRows(action string, paths []string, reason string, color func(string) string) []changeRow {
	rows := make([]changeRow, 0, len(paths))
	for _, rel := range paths {
		rows = append(rows, changeRow{action: action, path: displayRel(rel, false), detail: reason, color: color})
	}
	return rows
}

// printChanges lists the changes and returns how many it listed.
func printChanges(out *terminal.Writer, changes restore.Changes) int {
	rows := slices.Concat(
		withColor(groupedRows("restore", changes.Written, changes.Dirs), out.Green),
		withColor(groupedRows("remove", changes.Removed, changes.Dirs), out.Red),
	)
	printRows(out, rows)
	return rowsCount(rows)
}

// restoredCount returns how many of the listed changes the restore made.
func restoredCount(applied restore.Changes) int {
	notWritten := slices.Concat(applied.Lost, applied.Skipped)
	written := slices.DeleteFunc(slices.Clone(applied.Written), func(rel string) bool { return slices.Contains(notWritten, rel) })
	return rowsCount(groupedRows("", written, applied.Dirs)) + rowsCount(groupedRows("", applied.Removed, applied.Dirs))
}

func withColor(rows []changeRow, color func(string) string) []changeRow {
	for i := range rows {
		rows[i].color = color
	}
	return rows
}

func rowsCount(rows []changeRow) int {
	total := 0
	for _, row := range rows {
		total += row.count
	}
	return total
}

// groupedRows returns one row for each top-level path. A folder with changes
// inside it is shown as one row with the number of those changes, which
// counts the folder itself only when nothing inside it changed.
func groupedRows(action string, paths []string, dirs map[string]bool) []changeRow {
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
		inside := slices.DeleteFunc(groups[top], func(rel string) bool { return rel == top })
		switch {
		case len(inside) == 0:
			rows = append(rows, changeRow{action: action, path: displayRel(top, dirs[top]), count: 1})
		case len(inside) == 1 && !dirs[top] && len(groups[top]) == 1:
			rows = append(rows, changeRow{action: action, path: displayRel(inside[0], dirs[inside[0]]), count: 1})
		default:
			rows = append(rows, changeRow{action: action, path: displayRel(top, true), detail: pluralize(len(inside), "path"), count: len(inside)})
		}
	}
	return rows
}

// displayRel returns rel safe to print, ending in "/" when it is a folder.
func displayRel(rel string, isDir bool) string {
	if isDir {
		return terminal.Printable(rel) + "/"
	}
	return terminal.Printable(rel)
}

func confirm(env Env, prompt string) bool {
	fmt.Fprint(env.Stdout, prompt)
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil {
		// The input ended without an answer, and the prompt still waits for
		// its line.
		fmt.Fprintln(env.Stdout)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
