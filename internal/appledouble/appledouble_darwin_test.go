package appledouble

import (
	"os"
	"testing"

	"github.com/shimlabs/undo/internal/testutil"
)

func TestIsUsedOnlyWhereAttributesAreNotNative(t *testing.T) {
	exFAT := testutil.ExFATDir(t)
	if !IsUsedIn(exFAT) {
		t.Error("IsUsedIn(exFAT) = false")
	}
	dir, err := os.Open(exFAT)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if !IsUsedAt(int(dir.Fd())) {
		t.Error("IsUsedAt(exFAT) = false")
	}
	if IsUsedIn(testutil.TempDir(t)) {
		t.Error("IsUsedIn(APFS) = true")
	}
}
