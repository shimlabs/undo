package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
	"github.com/shimlabs/undo/internal/watch"
)

func TestRestoreRefusesToAskWithoutTerminal(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	testutil.WriteFile(t, filepath.Join(root, "f.txt"), "changed")
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pipeReader.Close() })
	pipeWriter.Close()
	var out bytes.Buffer
	code := Run([]string{"to", "1"}, Env{
		Dir: root, Stdin: pipeReader, Stdout: &out, Stderr: &out,
		StoreHome: defaultStoreHome(t), SocketDir: watch.DefaultSocketDir(),
	})

	if code != 1 || !strings.Contains(out.String(), "Run undo to 1 --yes to restore without asking.") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	testutil.AssertContent(t, filepath.Join(root, "f.txt"), "changed")
}

func TestRestoreWithNothingToDoTakesNoCheckpoint(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")

	out := mustRun(t, root, "--yes")
	if !strings.Contains(out, "Nothing to restore") || !strings.Contains(out, "Run undo list to pick an earlier checkpoint") {
		t.Fatalf("undo --yes with nothing to do:\n%s", out)
	}
	if list := mustRun(t, root, "list"); strings.Contains(list, "  2  ") {
		t.Fatalf("a restore with nothing to do took a checkpoint:\n%s", list)
	}
}

func TestRestoreTotalsAddUpRows(t *testing.T) {
	root := newProject(t)
	for _, rel := range []string{"src/a.ts", "src/b.ts", "src/lib/c.ts"} {
		testutil.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), "code")
	}
	mustRun(t, root, "checkpoint")
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(root, "notes.txt"), "new")
	mustRun(t, root, "checkpoint")

	out := mustRun(t, root, "diff", "1")
	for _, want := range []string{"restore  src/       3 paths", "remove   notes.txt", "Run undo to 1 to restore these 4 paths."} {
		if !strings.Contains(out, want) {
			t.Errorf("diff does not show %q:\n%s", want, out)
		}
	}
	if out := mustRun(t, root, "to", "1", "--yes"); !strings.Contains(out, "Restored 4 paths.") {
		t.Errorf("restore:\n%s", out)
	}
}

func TestUndoNamesExcludedFolderItCannotBringBack(t *testing.T) {
	root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	mustRun(t, root, "checkpoint")
	if err := os.RemoveAll(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, root, "--yes")
	if !strings.Contains(out, "node_modules/ is excluded from checkpoints, so undo cannot bring it back.") || strings.Contains(out, "already matches") {
		t.Fatalf("undo after removing node_modules:\n%s", out)
	}
}

func TestRestoreNamesExcludedFolderItLeftOut(t *testing.T) {
	root := newProject(t)
	testutil.WriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	mustRun(t, root, "checkpoint")
	for _, rel := range []string{"node_modules", "f.txt"} {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}

	out := mustRun(t, root, "--yes")
	if !strings.Contains(out, "Restored 1 path.") || !strings.Contains(out, "node_modules/ is excluded from checkpoints") {
		t.Fatalf("undo after removing node_modules and f.txt:\n%s", out)
	}
}

func makeChangedFileUnreadable(t *testing.T, root string) {
	t.Helper()
	testutil.WriteFile(t, filepath.Join(root, "f.txt"), "changed")
	if err := os.Chmod(filepath.Join(root, "f.txt"), 0o000); err != nil {
		t.Fatal(err)
	}
	testutil.RestoreModeOnCleanup(t, filepath.Join(root, "f.txt"), 0o644)
}

func TestDiffShowsPathsTheRestoreKeeps(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	makeChangedFileUnreadable(t, root)

	out := mustRun(t, root, "diff", "1")
	if !strings.Contains(out, "kept  f.txt  undo cannot read it") || !strings.Contains(out, "Nothing undo can restore; fix the kept path above and run undo again.") || strings.Contains(out, "already matches") {
		t.Fatalf("diff with only an unreadable change:\n%s", out)
	}
}

func TestUndoShowsKeptPathsBeforeAsking(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	makeChangedFileUnreadable(t, root)
	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "x")

	_, out := run(t, root, "n\n", "to", "1")
	kept, prompt := strings.Index(out, "kept"), strings.Index(out, "[y/N]")
	if kept < 0 || prompt < 0 || kept > prompt {
		t.Fatalf("the kept row is not shown before the question:\n%s", out)
	}
}

func TestDiffOfOnePathSaysRestoreIt(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	testutil.WriteFile(t, filepath.Join(root, "new.txt"), "x")
	if out := mustRun(t, root, "diff", "1"); !strings.Contains(out, "Run undo to 1 to restore it.") {
		t.Fatalf("diff of one path:\n%s", out)
	}
}

func TestRestoreThatOnlyKeepsPathsExitsNonZero(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	makeChangedFileUnreadable(t, root)

	if code, out := run(t, root, "", "--yes"); code != 1 || !strings.Contains(out, "Nothing undo can restore") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := run(t, root, "", "diff", "1"); code != 0 {
		t.Fatalf("diff exit %d:\n%s", code, out)
	}
}

// Agents take a checkpoint before read-only commands too, so by the time
// undo runs the newest checkpoint often matches the project. Plain undo then
// goes back to the newest checkpoint the project differs from.
func TestUndoSkipsCheckpointsThatMatchTheProject(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint", "--label", "rm f.txt")
	if err := os.Remove(filepath.Join(root, "f.txt")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, root, "checkpoint", "--label", "ls")
	mustRun(t, root, "checkpoint", "--label", "git status")

	out := mustRun(t, root, "--yes")
	if !strings.Contains(out, "Checkpoints 2–3 match the project now, so undo goes back to 1.") {
		t.Errorf("undo does not say which checkpoints it skipped:\n%s", out)
	}
	testutil.AssertContent(t, filepath.Join(root, "f.txt"), "keep me")
}

func TestUndoSaysNothingToRestoreWhenNoCheckpointDiffers(t *testing.T) {
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	mustRun(t, root, "checkpoint")

	if out := mustRun(t, root, "--yes"); !strings.Contains(out, "Nothing to restore: the project already matches checkpoint 2.") {
		t.Errorf("undo with no differing checkpoint:\n%s", out)
	}
}
