package terminal

import (
	"bytes"
	"testing"
)

func TestWriterLeavesOutColorsWhenNotATerminal(t *testing.T) {
	var out bytes.Buffer
	writer := NewWriter(&out)
	writer.Headingf("Checkpoint %d", 3)
	writer.Successf("Restored %s", writer.Green("2 paths"))
	writer.Warningf("watch out")
	writer.Errorf("failed")

	want := "==> Checkpoint 3\n✔ Restored 2 paths\nWarning: watch out\nError: failed\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestWriterColorsATerminal(t *testing.T) {
	writer := &Writer{isColor: true}
	if got := writer.Green("restore"); got != "\x1b[32mrestore\x1b[0m" {
		t.Fatalf("Green = %q", got)
	}
}

func TestIsColorEnabled(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		isTerminal bool
		want       bool
	}{
		{"terminal", map[string]string{"TERM": "xterm-256color"}, true, true},
		{"not a terminal", map[string]string{"TERM": "xterm-256color"}, false, false},
		{"NO_COLOR set", map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}, true, false},
		{"dumb terminal", map[string]string{"TERM": "dumb"}, true, false},
	}
	for _, test := range tests {
		getenv := func(key string) string { return test.env[key] }
		if got := isColorEnabled(getenv, test.isTerminal); got != test.want {
			t.Errorf("%s: isColorEnabled = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestPrintableEscapesControlCharacters(t *testing.T) {
	if got := Printable("rm -rf build"); got != "rm -rf build" {
		t.Fatalf("Printable changed plain text: %q", got)
	}
	for _, text := range []string{"echo \x1b]0;pwned\x07", "a\x9bb", "invoice\u202egpj.exe"} {
		if got := Printable(text); got == text {
			t.Errorf("Printable(%q) left the text unescaped", text)
		}
	}
}
