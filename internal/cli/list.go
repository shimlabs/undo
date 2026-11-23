package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/shell"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
)

// newestShown is how many checkpoints undo list shows without --all.
const newestShown = 20

func listCommand(parsed flags, env Env) error {
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
	out := env.stdout()
	older := 0
	if !parsed.isAll && len(checkpoints) > newestShown {
		older = len(checkpoints) - newestShown
		checkpoints = checkpoints[older:]
	}
	printCheckpoints(out, checkpoints, projectStore.Root())
	if older > 0 {
		out.Println()
		out.Printf("%s older. Run %s to see them.\n", pluralize(older, "checkpoint"), out.Bold(projectCommand(parsed, "undo list --all")))
	}
	return nil
}

// printCheckpoints lists checkpoints in a table: id, age, the command that
// followed, and where and by which agent it ran when that adds something.
func printCheckpoints(out *terminal.Writer, checkpoints []store.Checkpoint, root string) {
	out.Headingf("Checkpoints in %s", displayPath(root))
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
			details = append(details, terminal.Printable(checkpoint.Agent))
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
	// Stores record resolved roots, so a folder reached through a symlink,
	// such as /tmp on macOS, is matched by its real path.
	parent := env.Dir
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	stores, err := store.StoresUnder(env.StoreHome, parent)
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
		out.Warningf("%s was deleted. Bring it back with undo to %d and --project set to that path.", shown, latest.ID)
		return
	}
	out.Warningf("%s was deleted. Bring it back with:", displayPath(root))
	out.Printf("  %s\n", out.Bold(fmt.Sprintf("undo to %d --project %s", latest.ID, shell.Quote(root))))
}
