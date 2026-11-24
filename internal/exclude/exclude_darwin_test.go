package exclude

import (
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

// macOS makes "._" files on exFAT for the extended attributes of the files
// beside them, so they are not the project's own.
func TestNewExcludesAppleDoubleFilesOnExFAT(t *testing.T) {
	matcher, err := New(testutil.ExFATDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if !matcher.Match("src/._main.go", false) {
		t.Errorf("patterns %v do not exclude ._main.go on exFAT", matcher.Patterns())
	}
}

func TestNewKeepsAppleDoubleNamesWhereAttributesAreNative(t *testing.T) {
	matcher, err := New(testutil.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if matcher.Match("src/._main.go", false) {
		t.Errorf("patterns %v exclude ._main.go on APFS", matcher.Patterns())
	}
}
