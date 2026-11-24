package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/shimlabs/undo/internal/clone"
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
	out.Printf("Run %s to restore %s.\n", out.Bold(projectCommand(env, parsed, "undo to "+strconv.Itoa(checkpoint.ID))),
		pluralWord(count, "it", "these "+pluralize(count, "path")))
	return nil
}

// printNothingToRestore says why a restore has nothing to do: the project
// already matches the checkpoint, or what differs is kept or excluded.
// It reports whether the project already matches.
func printNothingToRestore(out *terminal.Writer, changes restore.Changes, id int) bool {
	switch {
	case len(changes.Kept) > 0:
		out.Println()
		out.Warningf("Nothing undo can restore; fix the kept %s above and run undo again.", pluralWord(len(changes.Kept), "path", "paths"))
	case len(changes.MissingExcluded) == 0:
		out.Successf("Nothing to restore: the project already matches checkpoint %d.", id)
		return true
	}
	return false
}

// findCheckpoint opens the project's store and finds the checkpoint the
// first argument names. A project with no checkpoints gets an error that
// says how to take one.
func findCheckpoint(env Env, parsed flags) (*store.Store, store.Checkpoint, error) {
	id, err := strconv.Atoi(parsed.args[0])
	if err != nil || id < 1 {
		return nil, store.Checkpoint{}, usageError{message: fmt.Sprintf("%q is not a checkpoint id, which undo list shows", parsed.args[0])}
	}
	projectStore, err := openStore(env, parsed)
	if isMissingProject(err) {
		return nil, store.Checkpoint{}, reportNoCheckpoints(env, parsed, id)
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

// reportNoCheckpoints explains that checkpoint id does not exist because the
// project has no checkpoints, and says how to take one. When the folder holds
// deleted projects instead, it lists them.
func reportNoCheckpoints(env Env, parsed flags, id int) error {
	root, err := findRootWithoutCheckpoints(env, parsed)
	if err != nil {
		return err
	}
	commandLine := "undo checkpoint"
	if !clone.IsSupportedIn(root) {
		commandLine += " --copy"
	}
	return withHint(fmt.Errorf("%w %d; %s has no checkpoints yet", store.ErrNoCheckpoint, id, displayPath(root)),
		fmt.Sprintf("Run %s to take one now.", env.stderr().Bold(projectCommand(env, parsed, commandLine))))
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
		if len(changes.Kept) > 0 {
			// The project still differs, so a script must not carry on as if
			// it had been restored.
			return errAlreadyReported
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
	out.Successf("Restored %s. Run %s to put %s back.", pluralize(count, "path"), out.Bold(projectCommand(env, parsed, "undo")), pluralWord(count, "it", "them"))
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
