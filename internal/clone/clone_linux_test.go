package clone

import "testing"

// A copy keeps what a clone on macOS keeps: the extended attributes of files
// and folders.
func TestCopyAtKeepsExtendedAttributes(t *testing.T) {
	checkCopyKeepsExtendedAttributes(t, t.TempDir(), "user.example.tag")
}
