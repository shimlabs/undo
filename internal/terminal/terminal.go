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
	// width is the terminal's width in columns, and 0 when out is not a
	// terminal.
	width int
}

// NewWriter returns a Writer for out, with colors when out is a terminal.
func NewWriter(out io.Writer) *Writer {
	file, isFile := out.(*os.File)
	isTerminal := isFile && term.IsTerminal(int(file.Fd()))
	width := 0
	if isTerminal {
		width, _, _ = term.GetSize(int(file.Fd()))
	}
	return &Writer{out: out, isColor: isColorEnabled(os.Getenv, isTerminal), width: width}
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
)

// Width returns the terminal's width in columns, and 0 when the output is
// not a terminal, where lines are not cut.
func (w *Writer) Width() int { return w.width }

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

// Printf writes formatted text as it is.
func (w *Writer) Printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// Println writes args as fmt.Println does.
func (w *Writer) Println(args ...any) {
	fmt.Fprintln(w.out, args...)
}

// Headingf writes a section heading: a blue arrow and bold text.
func (w *Writer) Headingf(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+blue, "==>"), w.Bold(fmt.Sprintf(format, args...)))
}

// Successf writes a line that reports something done, after a green check
// mark.
func (w *Writer) Successf(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+green, "✔"), fmt.Sprintf(format, args...))
}

// Warningf writes a line that needs the user's attention.
func (w *Writer) Warningf(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+yellow, "Warning:"), fmt.Sprintf(format, args...))
}

// Errorf writes a line that reports a failure.
func (w *Writer) Errorf(format string, args ...any) {
	fmt.Fprintf(w.out, "%s %s\n", w.style(bold+red, "Error:"), fmt.Sprintf(format, args...))
}

func (w *Writer) style(codes, text string) string {
	if !w.isColor || text == "" {
		return text
	}
	return codes + text + reset
}
