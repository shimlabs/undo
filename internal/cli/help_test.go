package cli

import (
	"strings"
	"testing"
)

func TestHelpListsEveryCommand(t *testing.T) {
	code, out := run(t, t.TempDir(), "", "--help")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, cmd := range commands() {
		if cmd.group != "" && !strings.Contains(out, cmd.synopsis()) {
			t.Errorf("help does not list %q", cmd.synopsis())
		}
	}
}

func TestCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"help", "to"}, {"to", "--help"}, {"to", "-h"}} {
		code, out := run(t, t.TempDir(), "", args...)
		if code != 0 || !strings.Contains(out, "undo to <id> [flags]") || !strings.Contains(out, "--project <path>") {
			t.Errorf("undo %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"lsit"}, `Unknown command "lsit". Did you mean undo list?`},
		{[]string{"list", "--copy"}, "undo list does not take --copy"},
		{[]string{"list", "--verbose"}, "Unknown flag --verbose."},
		{[]string{"status", "extra"}, "undo status takes no arguments"},
		{[]string{"diff", "1", "2"}, "Too many arguments for undo diff <id>."},
		{[]string{"help", "watch"}, `Unknown command "watch".`},
		{[]string{"rm"}, `Unknown command "rm".`},
	}
	for _, test := range tests {
		code, out := run(t, t.TempDir(), "", test.args...)
		if code != 2 || !strings.Contains(out, test.want) {
			t.Errorf("undo %s: exit %d, want 2 and %q\n%s", strings.Join(test.args, " "), code, test.want, out)
		}
	}
}

func TestUsageErrorShowsCommandUsage(t *testing.T) {
	_, out := run(t, t.TempDir(), "", "to")
	want := "Error: undo to needs a checkpoint id, which undo list shows.\nUsage: undo to <id> [flags]\nRun undo help to for more.\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"list", "list", 0},
		{"lsit", "list", 1},
		{"stauts", "status", 1},
		{"", "to", 2},
		{"checkpoint", "diff", 9},
	}
	for _, test := range tests {
		if got := editDistance(test.a, test.b); got != test.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestWrap(t *testing.T) {
	lines := wrap("one two three four", 9)
	if strings.Join(lines, "|") != "one two|three|four" {
		t.Fatalf("wrap = %q", lines)
	}
}

func TestListAlias(t *testing.T) {
	if code, out := run(t, t.TempDir(), "", "ls", "--help"); code != 0 || !strings.Contains(out, "undo list [flags]") {
		t.Fatalf("undo ls --help: exit %d\n%s", code, out)
	}
}
