package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/testutil"
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

func TestListShowsLabelAsTyped(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint", "--label", "before refactor")
	if out := mustRun(t, root, "list"); !strings.Contains(out, "before refactor") {
		t.Fatalf("list changed the label:\n%s", out)
	}
}

func TestOlderStoreSaysHowToStartOver(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	projectStore, err := store.Open(defaultStoreHome(t), root)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(projectStore.Dir(), "config.json")
	projectStore.Close()
	testutil.WriteFile(t, configPath, `{"format":1,"root":"`+root+`"}`)

	code, out := run(t, root, "", "list")
	if code != 1 || !strings.Contains(out, "written by an older version of undo") || !strings.Contains(out, "Move that folder aside") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
