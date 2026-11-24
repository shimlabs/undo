package tree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shimlabs/undo/internal/exclude"
	"github.com/shimlabs/undo/internal/testutil"
)

// On Btrfs a directory read can leave out a name a rename is replacing. A
// path the scan left out that still exists is added back, with what a
// folder holds; a path that is gone or excluded is not.
func TestAddMissedFindsPathsAScanLeftOut(t *testing.T) {
	root := testutil.TempDir(t)
	for _, rel := range []string{"src/main.go", "src/lib/util.go", "gone.txt", "node_modules/x.js"} {
		testutil.WriteFile(t, filepath.Join(root, rel), "x")
	}
	matcher, err := exclude.New(root)
	if err != nil {
		t.Fatal(err)
	}
	previous, _, err := Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	previous["node_modules"] = Entry{Kind: Dir}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	current, _, err := Scan(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"src/main.go", "src/lib", "src/lib/util.go"} {
		delete(current, rel)
	}

	if err := AddMissed(root, matcher, previous, current); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"src/main.go", "src/lib", "src/lib/util.go"} {
		if current[rel] != previous[rel] {
			t.Errorf("%s = %+v, want %+v", rel, current[rel], previous[rel])
		}
	}
	for _, rel := range []string{"gone.txt", "node_modules"} {
		if _, ok := current[rel]; ok {
			t.Errorf("AddMissed added %s, which is gone or excluded", rel)
		}
	}
}
