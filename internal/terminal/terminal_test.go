package terminal

import (
	"bytes"
	"testing"
)

func TestWriterLeavesOutColorsWhenNotATerminal(t *testing.T) {
	var out bytes.Buffer
	writer := NewWriter(&out)
	writer.Heading("Checkpoint %d", 3)
	writer.Success("Restored %s", writer.Green("2 paths"))
	writer.Warning("watch out")
	writer.Error("failed")

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
