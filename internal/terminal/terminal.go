// Package terminal formats undo's output for people: headings, symbols and
// colors in the style of Homebrew. Colors are only used when the output is a
// terminal and NO_COLOR is not set, so logs, pipes and tests get plain text.
package terminal

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Writer writes styled lines to one output.
type Writer struct {
	out     io.Writer
	isColor bool
}

// NewWriter returns a Writer for out, with colors when out is a terminal.
func NewWriter(out io.Writer) *Writer {
	return &Writer{out: out, isColor: isColorTerminal(out)}
}

// isColorTerminal reports whether out is a terminal that should get colors.
// See https://no-color.org for NO_COLOR.
func isColorTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	blue   = "\x1b[34m"
	cyan   = "\x1b[36m"
)

func (w *Writer) style(codes, text string) string {
	if !w.isColor || text == "" {
		return text
	}
	return codes + text + reset
}

// Bold returns text in bold.
func (w *Writer) Bold(text string) string { return w.style(bold, text) }

// Dim returns text dimmed, for details that matter less.
func (w *Writer) Dim(text string) string { return w.style(dim, text) }

// Green returns text in green, for what a restore writes back.
func (w *Writer) Green(text string) string { return w.style(green, text) }

// Red returns text in red, for what a restore removes or lost.
func (w *Writer) Red(text string) string { return w.style(red, text) }

// Yellow returns text in yellow, for what needs the user's attention.
func (w *Writer) Yellow(text string) string { return w.style(yellow, text) }

// Cyan returns text in cyan, for names such as agents.
func (w *Writer) Cyan(text string) string { return w.style(cyan, text) }

// Printf writes formatted text as it is.
func (w *Writer) Printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// Println writes a line as it is.
func (w *Writer) Println(line string) {
	fmt.Fprintln(w.out, line)
}

// Heading writes a section heading: a blue arrow and bold text.
func (w *Writer) Heading(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+blue, "==>"), w.Bold(fmt.Sprintf(format, args...)))
}

// Success writes a line that reports something done, after a green check
// mark.
func (w *Writer) Success(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+green, "✔"), fmt.Sprintf(format, args...))
}

// Warning writes a line that needs the user's attention.
func (w *Writer) Warning(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+yellow, "Warning:"), fmt.Sprintf(format, args...))
}

// Error writes a line that reports a failure.
func (w *Writer) Error(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+red, "Error:"), fmt.Sprintf(format, args...))
}
