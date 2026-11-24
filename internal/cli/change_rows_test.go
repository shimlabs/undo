package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shimlabs/undo/internal/terminal"
)

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
