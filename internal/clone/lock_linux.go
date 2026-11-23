package clone

import "os"

// IsLocked reports whether path has a flag that stops it from being changed.
// Linux keeps such flags only for privileged users, so undo treats no file
// as locked.
func IsLocked(string) bool {
	return false
}

// UnlockAt does nothing on Linux; see IsLocked.
func UnlockAt(*os.File, string) error {
	return nil
}
