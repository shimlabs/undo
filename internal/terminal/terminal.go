// Package terminal formats undo's output for people: headings, symbols and
// colors in the style of Homebrew. Colors are only used when the output is a
// terminal, NO_COLOR is not set and TERM is not dumb, so logs, pipes and tests
// get plain text. The Writer never escapes text; callers pass text from
// projects and agents through Printable.
package terminal

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Writer writes styled lines to one output.
type Writer struct {
	out     io.Writer
	isColor bool
}

// NewWriter returns a Writer for out, with colors when out is a terminal.
func NewWriter(out io.Writer) *Writer {
	file, isFile := out.(*os.File)
	isTerminal := isFile && term.IsTerminal(int(file.Fd()))
	return &Writer{out: out, isColor: isColorEnabled(os.Getenv, isTerminal)}
}

// isColorEnabled reports whether output to a terminal, or elsewhere, should
// get colors. See https://no-color.org for NO_COLOR.
func isColorEnabled(getenv func(string) string, isTerminal bool) bool {
	return isTerminal && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// Printable returns text safe to print to a terminal. Labels and paths come
// from agents and projects, and control characters in them could rewrite what
// the terminal shows.
func Printable(text string) string {
	isPlain := utf8.ValidString(text) && strings.IndexFunc(text, func(r rune) bool { return !strconv.IsPrint(r) }) < 0
	if isPlain {
		return text
	}
	return strconv.Quote(text)
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

// Dim returns text dimmed.
func (w *Writer) Dim(text string) string { return w.style(dim, text) }

// Green returns text in green.
func (w *Writer) Green(text string) string { return w.style(green, text) }

// Red returns text in red.
func (w *Writer) Red(text string) string { return w.style(red, text) }

// Yellow returns text in yellow.
func (w *Writer) Yellow(text string) string { return w.style(yellow, text) }

// Cyan returns text in cyan.
func (w *Writer) Cyan(text string) string { return w.style(cyan, text) }

// Printf writes formatted text as it is.
func (w *Writer) Printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// Println writes args as fmt.Println does.
func (w *Writer) Println(args ...any) {
	fmt.Fprintln(w.out, args...)
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
