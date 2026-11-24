package appledouble

import "golang.org/x/sys/unix"

// IsUsedIn reports whether macOS keeps extended attributes in AppleDouble
// files on the filesystem that holds dir.
func IsUsedIn(dir string) bool {
	var stat unix.Statfs_t
	return unix.Statfs(dir, &stat) == nil && isFilesystem(unix.ByteSliceToString(stat.Fstypename[:]))
}

// IsUsedAt reports what IsUsedIn does for the open directory dirFd.
func IsUsedAt(dirFd int) bool {
	var stat unix.Statfs_t
	return unix.Fstatfs(dirFd, &stat) == nil && isFilesystem(unix.ByteSliceToString(stat.Fstypename[:]))
}

// isFilesystem reports whether typeName names a filesystem without extended
// attributes of its own.
func isFilesystem(typeName string) bool {
	switch typeName {
	case "msdos", "exfat", "smbfs", "webdav":
		return true
	}
	return false
}
