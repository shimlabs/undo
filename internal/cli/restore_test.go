package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/terminal"
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
	if data, err := os.ReadFile(filepath.Join(root, "f.txt")); err != nil || string(data) != "changed" {
		t.Fatalf("f.txt = %q, %v, want it untouched", data, err)
	}
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

func TestChangeRows(t *testing.T) {
	written := []string{".", "README.md", "src", "src/main.go", "src/util.go", "docs", "lib/a.go", "web"}
	removed := []string{"web/old.js"}
	dirs := map[string]bool{"src": true, "docs": true, "web": true}
	writtenRows, removedRows := changeRows(written, removed, dirs)
	want := []changeRow{
		{path: "README.md", count: 1},
		{path: "src/", detail: "2 paths", count: 2},
		{path: "docs/", count: 1},
		{path: "lib/a.go", count: 1},
	}
	isEqual := func(a, b changeRow) bool {
		return a.path == b.path && a.detail == b.detail && a.count == b.count
	}
	if !slices.EqualFunc(writtenRows, want, isEqual) {
		t.Fatalf("written rows = %+v, want %+v", writtenRows, want)
	}
	if len(removedRows) != 1 || removedRows[0].path != "web/old.js" {
		t.Fatalf("removed rows = %+v, want only web/old.js, with web/ under no action", removedRows)
	}
	if total := rowsCount(writtenRows) + rowsCount(removedRows); total != 6 {
		t.Fatalf("rowsCount = %d, want 6, the sum of the rows", total)
	}
}

func TestPrintRowsAlignsDetailsAfterAccentedPaths(t *testing.T) {
	var out bytes.Buffer
	noColor := func(text string) string { return text }
	printRows(terminal.NewWriter(&out), []changeRow{
		{action: "restore", path: "café/", detail: "3 files", color: noColor},
		{action: "restore", path: "main/", detail: "2 files", color: noColor},
	})
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || utf8.RuneCountInString(lines[0]) != utf8.RuneCountInString(lines[1]) {
		t.Fatalf("details are not aligned:\n%s", out.String())
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
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := newProject(t)
	mustRun(t, root, "checkpoint")
	makeChangedFileUnreadable(t, root)

	out := mustRun(t, root, "diff", "1")
	if !strings.Contains(out, "kept  f.txt  undo cannot read it") || !strings.Contains(out, "Nothing undo can restore") || strings.Contains(out, "already matches") {
		t.Fatalf("diff with only an unreadable change:\n%s", out)
	}
}

func TestUndoShowsKeptPathsBeforeAsking(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
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
