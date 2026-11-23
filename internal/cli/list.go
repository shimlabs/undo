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
		return printNoCheckpoints(env, parsed)
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
		return printNoCheckpoints(env, parsed)
	}
	out := env.stdout()
	older := 0
	if !parsed.isAll && len(checkpoints) > newestShown {
		older = len(checkpoints) - newestShown
		checkpoints = checkpoints[older:]
	}
	printCheckpoints(out, checkpoints, projectStore.Root())
	if older > 0 {
		// The note goes to stderr, so the last line of the output is still
		// the newest checkpoint.
		errOut := env.stderr()
		verb := "are"
		if older == 1 {
			verb = "is"
		}
		errOut.Printf("%s %s not shown. Run %s to see them.\n", pluralize(older, "older checkpoint"), verb, errOut.Bold(projectCommand(env, parsed, "undo list --all")))
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
	prefixWidth := 2 + idWidth + 2 + ageWidth + 2
	for i, checkpoint := range checkpoints {
		label, details := fitRow(displayLabel(checkpoint.Label), checkpointDetails(checkpoint, root), out.Width()-prefixWidth)
		line := fmt.Sprintf("  %s  %s  %s", out.Bold(fmt.Sprintf("%*d", idWidth, checkpoint.ID)), out.Dim(fmt.Sprintf("%-*s", ageWidth, ages[i])), label)
		if details != "" {
			line += "  " + out.Dim(details)
		}
		out.Println(line)
	}
}

// minLabelWidth is the narrowest a label is cut to before its details are
// dropped to make room.
const minLabelWidth = 20

// fitRow cuts label so that it and details fit in width characters. When
// even that leaves the label too short, it drops the details instead. A
// width of 0 or less leaves both whole.
func fitRow(label, details string, width int) (string, string) {
	if width <= 0 {
		return label, details
	}
	room := width
	if details != "" {
		room = width - utf8.RuneCountInString(details) - 2
	}
	if details != "" && room < minLabelWidth {
		return truncate(label, width), ""
	}
	return truncate(label, room), details
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

// printNoCheckpoints says there are no checkpoints for the project and how
// to take one. When a deleted project directly inside the folder has some,
// it says how to bring that back instead, since running undo in the parent
// of a deleted folder is how people look for it.
func printNoCheckpoints(env Env, parsed flags) error {
	out := env.stdout()
	if printDeletedProjects(env) {
		return nil
	}
	root, err := projectRoot(env, parsed)
	if err == nil && parsed.project != "" {
		if _, statErr := os.Stat(root); errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("%s %w", displayPath(root), errNoSuchProject)
		}
	}
	if errors.Is(err, project.ErrNotProject) {
		out.Println(sentence(notProjectMessage(err)))
		out.Printf("Run %s inside a project folder.\n", out.Bold("undo"))
		return nil
	}
	if err != nil {
		root = env.Dir
	}
	out.Printf("No checkpoints yet in %s.\n", displayPath(root))
	if installed, _ := agentHooks(); len(installed) == 0 {
		out.Printf("Run %s to take one before every agent command, or %s to take one now.\n", out.Bold("undo install <agent>"), out.Bold("undo checkpoint"))
		return nil
	}
	out.Printf("The next agent command takes one, or run %s to take one now.\n", out.Bold("undo checkpoint"))
	return nil
}

// printDeletedProjects says how to bring back each deleted project directly
// inside the folder, and reports whether there was any.
func printDeletedProjects(env Env) bool {
	// Stores record resolved roots, so a folder reached through a symlink,
	// such as /tmp on macOS, is matched by its real path.
	parent := env.Dir
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	stores, err := store.StoresUnder(env.StoreHome, parent)
	if err != nil {
		return false
	}
	hasDeleted := false
	for _, other := range stores {
		if printDeletedProject(env.stdout(), other) {
			hasDeleted = true
		}
		other.Close()
	}
	return hasDeleted
}

// printDeletedProject says how to bring back the project of projectStore if
// its folder was deleted, and reports whether it was.
func printDeletedProject(out *terminal.Writer, projectStore *store.Store) bool {
	root := projectStore.Root()
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	latest, hasLatest, err := projectStore.Latest()
	if err != nil || !hasLatest {
		return false
	}
	if shown := terminal.Printable(root); shown != root {
		// A path with control characters cannot be shown both readably
		// and safe to paste, so the command is described instead.
		out.Warningf("%s was deleted. Bring it back with undo to %d and --project set to that path.", shown, latest.ID)
		return true
	}
	out.Headingf("%s was deleted", displayPath(root))
	out.Printf("Its newest checkpoint is %d, taken %s. Bring it back with:\n", latest.ID, timeAgo(latest.Time))
	out.Printf("  %s\n", out.Bold(fmt.Sprintf("undo to %d --project %s", latest.ID, shellPath(root))))
	return true
}

// shellPath returns path as a shell word, in its short ~ form when that needs
// no quoting.
func shellPath(path string) string {
	if shown := displayPath(path); strings.HasPrefix(shown, "~/") && shell.Join([]string{shown[2:]}) == shown[2:] {
		return shown
	}
	// The path is typed by the user, but control characters in it would
	// still rewrite what the terminal shows.
	return terminal.Printable(shell.Join([]string{path}))
}
