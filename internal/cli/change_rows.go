package cli

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/restore"
	"github.com/shimlabs/undo/internal/terminal"
)

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

func filter(paths []string, isIncluded func(string) bool) []string {
	return slices.DeleteFunc(slices.Clone(paths), func(rel string) bool { return !isIncluded(rel) })
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
