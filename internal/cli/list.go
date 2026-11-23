package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/hook"
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
// followed, and where and by which agent it ran when that adds something. On
// a terminal, each line is cut to its width.
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
		prefixWidth := 2 + idWidth + 2 + ageWidth + 2
		text := displayLabel(checkpoint.Label)
		if details := checkpointDetails(checkpoint, root); details != "" {
			text += "  " + details
		}
		text = truncate(text, out.Width()-prefixWidth)
		label, details, _ := strings.Cut(text, "  ")
		line := fmt.Sprintf("  %s  %s  %s", out.Bold(fmt.Sprintf("%*d", idWidth, checkpoint.ID)), out.Dim(fmt.Sprintf("%-*s", ageWidth, ages[i])), label)
		if details != "" {
			line += "  " + out.Dim(details)
		}
		out.Println(line)
	}
}

// checkpointDetails says where a checkpoint's command ran, when that was not
// the project root, and which agent ran it.
func checkpointDetails(checkpoint store.Checkpoint, root string) string {
	var details []string
	if checkpoint.Dir != "" && checkpoint.Dir != root {
		details = append(details, "in "+displayPath(checkpoint.Dir))
	}
	if checkpoint.Agent != "" {
		title := checkpoint.Agent
		if agent, ok := hook.FindAgent(checkpoint.Agent); ok {
			title = agent.Title
		}
		details = append(details, "("+terminal.Printable(title)+")")
	}
	return strings.Join(details, " ")
}

// truncate cuts text to width characters, ending it with "…". A width of 0
// or less leaves it whole.
func truncate(text string, width int) string {
	if width <= 0 || utf8.RuneCountInString(text) <= width {
		return text
	}
	runes := []rune(text)
	return string(runes[:max(width-1, 0)]) + "…"
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
