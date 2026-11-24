package appledouble

// IsUsedIn reports whether extended attributes are kept in AppleDouble files
// on the filesystem that holds dir, which only macOS does.
func IsUsedIn(string) bool { return false }

// IsUsedAt reports what IsUsedIn does for the open directory dirFd.
func IsUsedAt(int) bool { return false }
