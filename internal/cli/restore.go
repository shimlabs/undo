package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
)

func undoCommand(parsed flags, env Env) error {
	projectStore, err := openStore(env, parsed)
	if isMissingProject(err) {
		return printNoCheckpoints(env, parsed)
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
		return printNoCheckpoints(env, parsed)
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
		printNothingToRestore(out, changes, checkpoint.ID)
		return nil
	}
	out.Println()
	paths := "these " + pluralize(count, "path")
	if count == 1 {
		paths = "it"
	}
	out.Printf("Run %s to restore %s.\n", out.Bold(projectCommand(env, parsed, "undo to "+strconv.Itoa(checkpoint.ID))), paths)
	return nil
}

// printNothingToRestore says why a restore has nothing to do: the project
// already matches the checkpoint, or what differs is kept or excluded.
// It reports whether the project already matches.
func printNothingToRestore(out *terminal.Writer, changes restore.Changes, id int) bool {
	switch {
	case len(changes.Kept) > 0:
		out.Println()
		paths := "paths"
		if len(changes.Kept) == 1 {
			paths = "path"
		}
		out.Warningf("Nothing undo can restore; fix the kept %s above and run undo again.", paths)
	case len(changes.MissingExcluded) == 0:
		out.Successf("Nothing to restore: the project already matches checkpoint %d.", id)
		return true
	}
	return false
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
	if errors.Is(err, project.ErrNotProject) {
		// The folder may still hold deleted projects to pick from.
		if printDeletedProjects(env) {
			return nil, store.Checkpoint{}, errAlreadyReported
		}
		return nil, store.Checkpoint{}, err
	}
	if errors.Is(err, fs.ErrNotExist) {
		if err := printNoCheckpoints(env, parsed); err != nil {
			return nil, store.Checkpoint{}, err
		}
		return nil, store.Checkpoint{}, errAlreadyReported
	}
	if err != nil {
		return nil, store.Checkpoint{}, err
	}
	checkpoint, err := projectStore.Checkpoint(id)
	if err != nil {
		projectStore.Close()
		if errors.Is(err, store.ErrNoCheckpoint) {
			err = withHint(err, fmt.Sprintf("Run %s to see the checkpoints.", env.stderr().Bold(projectCommand(env, parsed, "undo list"))))
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
		commandLine := "undo"
		if !isLatest {
			commandLine = "undo to " + strconv.Itoa(checkpoint.ID)
		}
		return withHint(errNeedsTerminal,
			fmt.Sprintf("Run %s to restore without asking.", env.stderr().Bold(projectCommand(env, parsed, commandLine+" --yes"))))
	}
	printCheckpointHeading(out, checkpoint, projectStore.Root())
	// The shell may still be in a folder the restore brings back.
	isShellFolderGone := parsed.project == "" && !exists(env.Dir)
	changes, err := planOrApply(env, parsed, projectStore, checkpoint.ID)
	if err != nil {
		return err
	}
	count := printChanges(out, changes)
	if count == 0 {
		if printNothingToRestore(out, changes, checkpoint.ID) && isLatest {
			out.Printf("Run %s to pick an earlier checkpoint, then %s.\n", out.Bold(projectCommand(env, parsed, "undo list")), out.Bold("undo to <id>"))
		}
		return nil
	}
	out.Println()
	shownKept := changes.Kept
	if !parsed.isConfirmed {
		if !confirm(env, out.Bold(fmt.Sprintf("Restore %s?", pluralize(count, "path")))+" [y/N] ") {
			out.Println("Nothing restored.")
			return errDeclined
		}
		if changes, err = restore.Apply(projectStore, checkpoint.ID, env.checkpointer()); err != nil {
			return err
		}
	}
	printRestored(env, parsed, changes, shownKept, isShellFolderGone)
	return nil
}

// printRestored reports what a restore did, what it left alone beyond the
// kept paths already shown, and how to take it back.
func printRestored(env Env, parsed flags, applied restore.Changes, shownKept []string, isShellFolderGone bool) {
	out := env.stdout()
	if printNotRestored(out, applied, shownKept) {
		out.Println()
	}
	count := restoredCount(applied)
	pronoun := "them"
	if count == 1 {
		pronoun = "it"
	}
	out.Successf("Restored %s. Run %s to put %s back.", pluralize(count, "path"), out.Bold(projectCommand(env, parsed, "undo")), pronoun)
	if isShellFolderGone && exists(env.Dir) {
		// The shell's folder is a new one now, which its old handle does not
		// show.
		out.Printf("Run %s to see the restored files.\n", out.Bold("cd ."))
	}
}

// planOrApply plans the restore of checkpoint id, or with --yes, where
// there is nothing to wait for, restores it at once, which plans only once.
func planOrApply(env Env, parsed flags, projectStore *store.Store, id int) (restore.Changes, error) {
	if parsed.isConfirmed {
		return restore.Apply(projectStore, id, env.checkpointer())
	}
	return restore.Plan(projectStore, id)
}

// projectCommand returns commandLine with the --project flag the user gave,
// so a suggested command acts on the same project. The path is shown short,
// with ~, when that needs no quoting.
func projectCommand(env Env, parsed flags, commandLine string) string {
	if parsed.project == "" {
		return commandLine
	}
	return commandLine + " --project " + shellPath(absolutePath(parsed.project, env.Dir))
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
	color  func(string) string
}

// maxPathColumnWidth keeps one long path from pushing every detail far to
// the right.
const maxPathColumnWidth = 48

// printRows writes rows as aligned columns.
func printRows(out *terminal.Writer, rows []changeRow) {
	actionWidth, pathWidth := 0, 0
	for _, row := range rows {
		actionWidth = max(actionWidth, len(row.action))
		pathWidth = max(pathWidth, utf8.RuneCountInString(row.path))
	}
	pathWidth = min(pathWidth, maxPathColumnWidth)
	for _, row := range rows {
		line := fmt.Sprintf("  %s  %s", row.color(fmt.Sprintf("%-*s", actionWidth, row.action)), row.path)
		if row.detail != "" {
			line += strings.Repeat(" ", max(pathWidth-utf8.RuneCountInString(row.path), 0)) + "  " + out.Dim(row.detail)
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
		restore.KeptExcluded:   "excluded since the checkpoint; remove the rule from " + exclude.IgnoreFile + " to restore it",
		restore.KeptOccupied:   "it holds files undo leaves alone, such as excluded ones",
	}
}

// printNotRestored lists what the restore left alone and why, except the
// kept paths already shown, and reports whether it listed anything.
func printNotRestored(out *terminal.Writer, applied restore.Changes, shownKept []string) bool {
	kept := slices.DeleteFunc(slices.Clone(applied.Kept), func(rel string) bool { return slices.Contains(shownKept, rel) })
	rows := keptRows(out, applied, kept)
	rows = append(rows, reasonRows("lost", applied.Lost, "its saved copy is gone, as after a power cut", out.Red)...)
	rows = append(rows, reasonRows("check", applied.Unstable, "it was changing while it was saved", out.Yellow)...)
	rows = append(rows, reasonRows("skipped", applied.Skipped, "sockets and devices cannot be restored", out.Dim)...)
	printRows(out, rows)
	return len(rows) > 0
}

func keptRows(out *terminal.Writer, changes restore.Changes, kept []string) []changeRow {
	reasons := keepReasons()
	rows := make([]changeRow, 0, len(kept))
	for _, rel := range kept {
		rows = append(rows, changeRow{action: "kept", path: displayRel(rel, changes.Dirs[rel]), detail: reasons[changes.KeepReasons[rel]], color: out.Yellow})
	}
	return rows
}

func reasonRows(action string, paths []string, reason string, color func(string) string) []changeRow {
	rows := make([]changeRow, 0, len(paths))
	for _, rel := range paths {
		rows = append(rows, changeRow{action: action, path: displayRel(rel, false), detail: reason, color: color})
	}
	return rows
}

// printChanges lists the changes, the paths the restore keeps, and the
// excluded paths it cannot bring back, and returns how many changes it
// listed.
func printChanges(out *terminal.Writer, changes restore.Changes) int {
	written, removed := changeRows(changes.Written, changes.Removed, changes.Dirs)
	restoreRows := slices.Concat(withColor(written, "restore", out.Green), withColor(removed, "remove", out.Red))
	rows := slices.Concat(restoreRows, keptRows(out, changes, changes.Kept))
	printRows(out, rows)
	if len(rows) > 0 && len(changes.MissingExcluded) > 0 {
		out.Println()
	}
	printMissingExcluded(out, changes)
	return rowsCount(restoreRows)
}

// maxNamedPaths is how many missing excluded paths are named before the
// rest are counted.
const maxNamedPaths = 3

// printMissingExcluded names the excluded paths the checkpoint had that are
// gone, which no restore brings back.
func printMissingExcluded(out *terminal.Writer, changes restore.Changes) {
	missing := changes.MissingExcluded
	if len(missing) == 0 {
		return
	}
	names := make([]string, 0, maxNamedPaths+1)
	for _, rel := range missing[:min(len(missing), maxNamedPaths)] {
		names = append(names, displayRel(rel, changes.Dirs[rel]))
	}
	if len(missing) > maxNamedPaths {
		names = append(names, fmt.Sprintf("%d more", len(missing)-maxNamedPaths))
	}
	verb, pronoun := "are", "them"
	if len(missing) == 1 {
		verb, pronoun = "is", "it"
	}
	out.Warningf("%s %s excluded from checkpoints, so undo cannot bring %s back. Reinstall or rebuild %s.", joinWithAnd(names), verb, pronoun, pronoun)
}

// joinWithAnd joins items as a list in a sentence: "a", "a and b", "a, b
// and c".
func joinWithAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// restoredCount returns how many of the listed changes the restore made.
func restoredCount(applied restore.Changes) int {
	written, removed := changeRows(applied.RestoredPaths(), applied.Removed, applied.Dirs)
	return rowsCount(written) + rowsCount(removed)
}

func withColor(rows []changeRow, action string, color func(string) string) []changeRow {
	for i := range rows {
		rows[i].action, rows[i].color = action, color
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

// changeRows returns the rows for the written and the removed paths. A folder
// with changes inside it, written or removed, stands for those changes
// rather than counting as one itself, so no folder shows under two actions.
func changeRows(written, removed []string, dirs map[string]bool) (writtenRows, removedRows []changeRow) {
	holdsChanges := map[string]bool{}
	for _, rel := range slices.Concat(written, removed) {
		for dir := path.Dir(rel); rel != "." && dir != "."; dir = path.Dir(dir) {
			holdsChanges[dir] = true
		}
	}
	isShown := func(rel string) bool { return rel != "." && (!dirs[rel] || !holdsChanges[rel]) }
	return groupedRows(filter(written, isShown), dirs), groupedRows(filter(removed, isShown), dirs)
}

func filter(paths []string, isKept func(string) bool) []string {
	return slices.DeleteFunc(slices.Clone(paths), func(rel string) bool { return !isKept(rel) })
}

// groupedRows returns one row for each top-level name: the path itself when
// it is the only one under that name, and otherwise the folder with the
// number of paths in it.
func groupedRows(paths []string, dirs map[string]bool) []changeRow {
	var order []string
	groups := map[string][]string{}
	for _, rel := range paths {
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
			rows = append(rows, changeRow{path: displayRel(members[0], dirs[members[0]]), count: 1})
			continue
		}
		rows = append(rows, changeRow{path: displayRel(top, true), detail: pluralize(len(members), "path"), count: len(members)})
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
