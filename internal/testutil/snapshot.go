package testutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// SnapshotOptions choose what Snapshot describes.
type SnapshotOptions struct {
	// ShouldIncludeDirTimes adds each directory's modification time.
	ShouldIncludeDirTimes bool
	// ShouldSkipExcluded leaves out the paths inside a node_modules
	// directory, which a restore never touches. Directory times are then
	// best left out too, since an excluded change also moves them.
	ShouldSkipExcluded bool
}

// excludedDirName is the excluded directory that Mutator writes into.
const excludedDirName = "node_modules"

// Snapshot describes every path under root that undo restores: its kind,
// permissions, symlink target, and for a file its modification time and
// content.
func Snapshot(tb testing.TB, root string, options SnapshotOptions) map[string]string {
	tb.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if options.ShouldSkipExcluded && slices.Contains(strings.Split(rel, string(filepath.Separator)), excludedDirName) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entries[rel], err = describe(path, info, options.ShouldIncludeDirTimes)
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	return entries
}

func describe(path string, info fs.FileInfo, shouldIncludeDirTimes bool) (string, error) {
	mode := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return "link " + target, err
	case info.Mode()&fs.ModeNamedPipe != 0:
		return fmt.Sprintf("fifo %v", mode), nil
	case info.IsDir() && shouldIncludeDirTimes:
		return fmt.Sprintf("dir %v %d", mode, info.ModTime().UnixNano()), nil
	case info.IsDir():
		return fmt.Sprintf("dir %v", mode), nil
	default:
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Sprintf("file %v %d unreadable, %d bytes", mode, info.ModTime().UnixNano(), info.Size()), nil
		}
		return fmt.Sprintf("file %v %d %q", mode, info.ModTime().UnixNano(), data), err
	}
}

// SnapshotDiff lists the paths whose description differs between want and
// got. A directory got has and want lacks is not listed when it holds an
// excluded node_modules, which a restore must leave in place.
func SnapshotDiff(want, got map[string]string) []string {
	var paths []string
	for rel, description := range want {
		if got[rel] != description {
			paths = append(paths, rel)
		}
	}
	for rel, description := range got {
		if _, isWanted := want[rel]; isWanted {
			continue
		}
		if !strings.HasPrefix(description, "dir ") || !holdsOnlyDirs(got, rel) {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	return paths
}

// holdsOnlyDirs reports whether the directory rel in snapshot has nothing in
// it but directories, which then must lead to excluded content.
func holdsOnlyDirs(snapshot map[string]string, rel string) bool {
	prefix := rel + string(filepath.Separator)
	for other, description := range snapshot {
		if strings.HasPrefix(other, prefix) && !strings.HasPrefix(description, "dir ") {
			return false
		}
	}
	return true
}
