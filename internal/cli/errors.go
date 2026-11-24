package cli

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/terminal"
)

// usageError is a mistake in how undo was called. command, when set, is the
// command whose usage the error shows.
type usageError struct {
	message string
	command *command
	// isUnknownCommand marks an error about which command to run, which
	// points to the list of commands rather than to one command's help.
	isUnknownCommand bool
}

func (e usageError) Error() string { return e.message }

// hintedError is a failure with the command that helps, shown on its own
// line under the error.
type hintedError struct {
	err  error
	hint string
}

func (e hintedError) Error() string { return e.err.Error() }

func (e hintedError) Unwrap() error { return e.err }

// withHint returns err with hint, a sentence that starts with a command the
// user can run.
func withHint(err error, hint string) error {
	return hintedError{err: err, hint: hint}
}

// commandError is a failure that sets an exit code of its own, as a shell
// does for a command it cannot find or run.
type commandError struct {
	err      error
	exitCode int
}

func (e commandError) Error() string { return e.err.Error() }

func (e commandError) Unwrap() error { return e.err }

var (
	errDeclined = errors.New("declined")
	// errAlreadyReported means the command explained the failure itself,
	// so only the exit code is left to set.
	errAlreadyReported = errors.New("already reported")
	// errNeedsTerminal means undo would ask before restoring, but no one
	// can answer.
	errNeedsTerminal = errors.New("undo asks before it restores, and its input is not a terminal")
	errNoSuchProject = errors.New("does not exist, and undo has no checkpoints of it")
	errFolderMissing = errors.New("does not exist")
)

// userMessage returns err as a sentence for people: a path in it is shown
// the way undo shows paths, without the name of the system call that
// failed, and it starts with a capital letter and ends with a period.
func userMessage(err error) string {
	message := err.Error()
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		shown := displayPath(pathErr.Path) + ": " + pathErr.Err.Error()
		// os.Root names the call openat.
		if pathErr.Op == "open" || pathErr.Op == "openat" {
			shown = "cannot open " + shown
		}
		message = strings.Replace(message, pathErr.Error(), shown, 1)
	}
	if linkErr, ok := errors.AsType[*os.LinkError](err); ok {
		message = strings.Replace(message, linkErr.Error(), displayPath(linkErr.New)+": "+linkErr.Err.Error(), 1)
	}
	return sentence(terminal.Printable(message))
}

// sentence capitalizes text, unless it starts with an undo command, and ends
// it with a period unless it ends with other punctuation.
func sentence(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	if first == utf8.RuneError {
		return text
	}
	if !strings.HasPrefix(text, "undo ") {
		text = string(unicode.ToUpper(first)) + text[size:]
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "!") {
		text += "."
	}
	return text
}

// printError writes err and, for a hinted error, its hint.
func printError(out *terminal.Writer, err error) {
	out.Errorf("%s", userMessage(err))
	if hinted, ok := errors.AsType[hintedError](err); ok {
		out.Println(hinted.hint)
	}
}

// printUsageError writes err, how its command is typed, and where to read
// more.
func printUsageError(out *terminal.Writer, err usageError) {
	out.Errorf("%s", sentence(terminal.Printable(err.message)))
	if err.command == nil || err.isUnknownCommand {
		out.Printf("Run %s for the list of commands.\n", out.Bold("undo help"))
		return
	}
	out.Printf("%s %s\n", out.Bold("Usage:"), err.command.usageLine())
	help := "undo help"
	if err.command.name != "" {
		help += " " + err.command.name
	}
	out.Printf("Run %s for more.\n", out.Bold(help))
}

// notProjectMessage says why undo refused the folder in err as a project:
// it is the user's home folder or the disk root.
func notProjectMessage(err error) string {
	notProject, ok := errors.AsType[*project.NotProjectError](err)
	switch {
	case !ok:
		return err.Error()
	case notProject.IsHome:
		return "~ is your home folder, not a project"
	default:
		return displayPath(notProject.Dir) + " is the disk root, not a project"
	}
}
