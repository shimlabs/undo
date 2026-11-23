package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
)

func TestPrintCheckpointsEscapesStoredText(t *testing.T) {
	var out bytes.Buffer
	checkpoint := store.Checkpoint{ID: 1, Time: time.Now(), CheckpointOptions: store.CheckpointOptions{
		Label: "echo \x1b]0;label\x07",
		Dir:   "/project/\x1b[2J",
		Agent: "claude\x1b]0;agent\x07\x1b[2J",
	}}
	printCheckpoints(terminal.NewWriter(&out), []store.Checkpoint{checkpoint}, "/project")
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("list printed a raw escape sequence:\n%q", out.String())
	}
}

func TestShellPathEscapesControlCharacters(t *testing.T) {
	if shown := shellPath("/tmp/q\x1b]0;title\a"); strings.ContainsAny(shown, "\x1b\a") {
		t.Fatalf("shellPath printed control characters raw: %q", shown)
	}
}
