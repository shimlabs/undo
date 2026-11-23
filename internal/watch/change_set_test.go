package watch

import (
	"maps"
	"slices"
	"testing"

	"github.com/shimlabs/undo/internal/tree"
)

func TestChangeSetRemovesEverythingUnderGoneDirectory(t *testing.T) {
	file := tree.Entry{Kind: tree.File, Size: 1}
	changes := newChangeSet(tree.Listing{
		".":           {Kind: tree.Dir},
		"src":         {Kind: tree.Dir},
		"src/main.go": file,
	})
	// A path below a directory renamed by case alone still stats on a
	// case-insensitive volume, under the old spelling.
	changes.update("src/main.go", tree.Entry{Kind: tree.File, Size: 2})
	changes.remove("src")
	changes.removeUnder("src")
	changes.finish()

	if _, ok := changes.listing["src/main.go"]; ok {
		t.Fatal("the listing kept a path under a directory that is gone")
	}
}

func TestChangeSetKeepsPathsFoundAgainInReplacedDirectory(t *testing.T) {
	file := tree.Entry{Kind: tree.File, Size: 1}
	changes := newChangeSet(tree.Listing{
		".":            {Kind: tree.Dir},
		"docs":         {Kind: tree.Dir},
		"docs/old.md":  file,
		"docs/kept.md": file,
	})
	changes.replaceUnder("docs")
	changes.update("docs/kept.md", file)
	changes.finish()

	if _, ok := changes.listing["docs/old.md"]; ok {
		t.Error("the listing kept a path the replacing directory does not have")
	}
	if _, ok := changes.listing["docs/kept.md"]; !ok {
		t.Error("the listing lost a path the replacing directory has")
	}
	if !slices.Equal(slices.Sorted(maps.Keys(changes.deleted)), []string{"docs/old.md"}) {
		t.Errorf("deleted = %v", changes.deleted)
	}
}
